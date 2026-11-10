package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/scott/castbot/internal/runtime"
)

const apiRoot = "https://api.digitalocean.com"

// Options configures the machine API client.
// Token is a writable machine token. Tests pass a stand-in and a BaseURL.
type Options struct {
	Token    string
	BaseURL  string
	HTTP     *http.Client
	Interval time.Duration
	RetryFor time.Duration
	// AddressFor is how long Create keeps polling after the API says running
	// while the hostname is still empty. Zero waits 60 seconds.
	AddressFor   time.Duration
	InsecureEdge bool
}

// MicroVM is the AgentHost that provisions one machine per agent.
type MicroVM struct {
	token      string
	base       string
	http       *http.Client
	edge       *http.Client
	interval   time.Duration
	retryFor   time.Duration
	addressFor time.Duration
	insecure   bool

	mu    sync.Mutex
	cache map[string]statusHit
}

type statusHit struct {
	at time.Time
	vm Machine
}

// New returns a client. It does not call the API.
func New(opt Options) (*MicroVM, error) {
	if strings.TrimSpace(opt.Token) == "" {
		return nil, errors.New("machine token is empty")
	}
	base := strings.TrimRight(opt.BaseURL, "/")
	if base == "" {
		base = apiRoot
	}
	client := opt.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	interval := opt.Interval
	if interval <= 0 {
		interval = time.Second
	}
	retryFor := opt.RetryFor
	if retryFor <= 0 {
		retryFor = 30 * time.Second
	}
	addressFor := opt.AddressFor
	if addressFor <= 0 {
		addressFor = 60 * time.Second
	}
	return &MicroVM{
		token:      opt.Token,
		base:       base,
		http:       client,
		interval:   interval,
		retryFor:   retryFor,
		addressFor: addressFor,
		insecure:   opt.InsecureEdge,
		edge: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Create posts a machine, polls until it is running or failed, and deletes
// a failed machine. The caller can retry. A cancelled poll deletes the
// machine it already created so it is not left running.
func (m *MicroVM) Create(ctx context.Context, spec CreateSpec) (Machine, error) {
	if err := validateSpec(spec); err != nil {
		return Machine{}, err
	}
	vm, err := m.postCreate(ctx, spec)
	if err != nil {
		return Machine{}, err
	}
	ready, err := m.poll(ctx, vm.ID)
	if err != nil {
		m.cleanup(vm.ID)
		return Machine{}, err
	}
	if ready.State == "failed" || ready.State == "terminated" {
		m.cleanup(ready.ID)
		return ready, &Failed{Reason: ready.Failure}
	}
	return ready, nil
}

// Status reads the machine API. It does not call the guest.
// A successful read is reused for a few seconds so a page load does not
// call the API again immediately. Pause, wake, and delete drop the cache.
func (m *MicroVM) Status(ctx context.Context, id string) (Machine, error) {
	if vm, ok := m.cached(id); ok {
		return vm, nil
	}
	vm, err := m.get(ctx, id)
	if err != nil {
		return Machine{}, err
	}
	m.remember(vm)
	return vm, nil
}

// Wake resumes a paused machine through the API.
func (m *MicroVM) Wake(ctx context.Context, id string) (Machine, error) {
	return m.action(ctx, id, "resume")
}

// Pause pauses a machine through the API.
func (m *MicroVM) Pause(ctx context.Context, id string) (Machine, error) {
	return m.action(ctx, id, "pause")
}

// Delete removes a machine. A 404 is success.
func (m *MicroVM) Delete(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" || id == "pending" {
		return errors.New("missing machine id")
	}
	res, err := m.do(ctx, http.MethodDelete, "/v2/microvms/"+url.PathEscape(id), nil, "")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	m.forget(id)
	if res.StatusCode == http.StatusNoContent || res.StatusCode == http.StatusOK || res.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil
	}
	return m.apiErr(res, "")
}

// List returns the machines visible to the token. The live harness uses it
// to prove a machine this process created is gone.
func (m *MicroVM) List(ctx context.Context) ([]Machine, error) {
	res, err := m.do(ctx, http.MethodGet, "/v2/microvms?per_page=200", nil, "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, m.apiErr(res, "")
	}
	var body struct {
		MicroVMs []apiVM `json:"microvms"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]Machine, 0, len(body.MicroVMs))
	for _, vm := range body.MicroVMs {
		out = append(out, vm.machine(m.insecure))
	}
	return out, nil
}

// Probe calls the edge the way the platform probe did: the machine token
// in Authorization and the agent token in X-Cast-Authorization.
// It retries while the guest is still booting. Status never calls Probe.
func (m *MicroVM) Probe(ctx context.Context, endpoint, agentToken string) error {
	if err := m.checkEdge(endpoint, agentToken); err != nil {
		return err
	}
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	var last string
	for {
		code, err := m.edgeOnce(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/castlet/health", agentToken, nil)
		if err == nil && code == http.StatusOK {
			return nil
		}
		if err != nil {
			last = "edge"
		} else {
			last = fmt.Sprintf("status %d", code)
			if code != http.StatusServiceUnavailable && code != http.StatusBadGateway && code != http.StatusGatewayTimeout {
				return fmt.Errorf("probe %s", last)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("probe %s", last)
		case <-tick.C:
		}
	}
}

// Stream opens message/send through the edge. Both tokens are set.
// A paused machine resumes because the edge accepts the request.
func (m *MicroVM) Stream(ctx context.Context, endpoint, agentToken string, req runtime.Send) (runtime.Stream, error) {
	if err := m.checkEdge(endpoint, agentToken); err != nil {
		return nil, err
	}
	c := runtime.NewHTTP(endpoint)
	c.SetToken(agentToken)
	c.SetEdgeToken(m.token)
	return c.Open(ctx, req)
}

// Approve posts permission/respond through the edge.
func (m *MicroVM) Approve(ctx context.Context, endpoint, agentToken, requestID string, allow bool) error {
	if strings.TrimSpace(requestID) == "" {
		return errors.New("missing request id")
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  "permission/respond",
		"params": map[string]any{
			"id":       requestID,
			"approved": allow,
		},
	})
	if err != nil {
		return err
	}
	if err := m.checkEdge(endpoint, agentToken); err != nil {
		return err
	}
	code, err := m.edgeOnce(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/", agentToken, payload)
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusNoContent && code != http.StatusAccepted {
		return fmt.Errorf("approve status %d", code)
	}
	return nil
}

// Cancel does not call the guest. The runtime has no cancel.
func (m *MicroVM) Cancel(context.Context, string, string, string) error {
	return ErrCancel
}

// PutFile writes one workspace file through the edge.
func (m *MicroVM) PutFile(ctx context.Context, endpoint, agentToken, name string, body io.Reader) error {
	rel, err := cleanName(name)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(body, (32<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 32<<20 {
		return errors.New("file is too large")
	}
	if err := m.checkEdge(endpoint, agentToken); err != nil {
		return err
	}
	code, err := m.edgeOnce(ctx, http.MethodPut, strings.TrimRight(endpoint, "/")+"/castlet/files/"+rel, agentToken, raw)
	if err != nil {
		return err
	}
	if code != http.StatusNoContent && code != http.StatusOK {
		return fmt.Errorf("put file status %d", code)
	}
	return nil
}

// GetFile reads one workspace file through the edge.
func (m *MicroVM) GetFile(ctx context.Context, endpoint, agentToken, name string) (io.ReadCloser, error) {
	rel, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	if err := m.checkEdge(endpoint, agentToken); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/castlet/files/"+rel, nil)
	if err != nil {
		return nil, err
	}
	m.edgeAuth(req, agentToken)
	res, err := m.edge.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("get file status %d", res.StatusCode)
	}
	return res.Body, nil
}

// Reload posts /castlet/reload and retries a 409 until the budget ends
// or the context is cancelled. The budget is about 30 seconds.
func (m *MicroVM) Reload(ctx context.Context, endpoint, agentToken string) error {
	if err := m.checkEdge(endpoint, agentToken); err != nil {
		return err
	}
	target := strings.TrimRight(endpoint, "/") + "/castlet/reload"
	budget := m.retryFor
	if budget <= 0 {
		budget = 30 * time.Second
	}
	wait := m.interval
	if wait <= 0 {
		wait = time.Second
	}
	deadline := time.Now().Add(budget)
	var last error
	for {
		code, err := m.edgeOnce(ctx, http.MethodPost, target, agentToken, nil)
		if err != nil {
			return err
		}
		if code == http.StatusOK || code == http.StatusNoContent {
			return nil
		}
		if code != http.StatusConflict {
			return fmt.Errorf("reload status %d", code)
		}
		last = errors.New("reload in progress")
		if time.Until(deadline) <= wait {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if wait < 8*time.Second {
			wait *= 2
		}
	}
}

func (m *MicroVM) poll(ctx context.Context, id string) (Machine, error) {
	tick := time.NewTicker(m.interval)
	defer tick.Stop()
	var waiting time.Time
	budget := m.addressFor
	if budget <= 0 {
		budget = 60 * time.Second
	}
	for {
		vm, err := m.get(ctx, id)
		if err != nil {
			return Machine{}, err
		}
		switch vm.State {
		case "failed", "terminated", "paused":
			return vm, nil
		case "running":
			// A running machine can still have an empty hostname. The API
			// publishes it only once the URL is ACTIVE. Keep polling until
			// it appears. A published hostname that fails the edge check is
			// rejected immediately and is not treated like a missing one.
			if vm.Endpoint != "" || vm.AddressRejected {
				return vm, nil
			}
			if waiting.IsZero() {
				waiting = time.Now()
			}
			if time.Since(waiting) >= budget {
				return vm, nil
			}
		}
		select {
		case <-ctx.Done():
			return Machine{}, ctx.Err()
		case <-tick.C:
		}
	}
}

func (m *MicroVM) cleanup(id string) {
	cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = m.Delete(cctx, id)
}

func (m *MicroVM) postCreate(ctx context.Context, spec CreateSpec) (Machine, error) {
	idle := spec.Idle
	if idle <= 0 {
		idle = DefaultIdle
	}
	body, err := json.Marshal(createReq{
		Name:     spec.Name,
		Region:   spec.Region,
		Size:     sizeReq{CPU: spec.CPU, Memory: spec.MemoryMiB},
		Source:   sourceReq{OCIRef: spec.Image},
		HTTPPort: spec.HTTPPort,
		AutoPause: autoReq{
			Idle: formatIdle(idle),
		},
		Tags: machineTags(spec),
		Env: map[string]string{
			"CASTBOT_URL":             spec.ControlURL,
			"CASTBOT_AGENT_ID":        spec.AgentID,
			"CASTBOT_BOOTSTRAP_TOKEN": spec.Bootstrap,
		},
	})
	if err != nil {
		return Machine{}, err
	}
	res, err := m.do(ctx, http.MethodPost, "/v2/microvms", body, spec.Bootstrap)
	if err != nil {
		return Machine{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		return Machine{}, m.apiErr(res, spec.Bootstrap)
	}
	vm, err := decodeVM(res.Body)
	if err != nil {
		return Machine{}, err
	}
	out := vm.machine(m.insecure)
	if out.ID == "" {
		return Machine{}, errors.New("machine id missing")
	}
	return out, nil
}

func (m *MicroVM) get(ctx context.Context, id string) (Machine, error) {
	if strings.TrimSpace(id) == "" || id == "pending" {
		return Machine{}, errors.New("missing machine id")
	}
	res, err := m.do(ctx, http.MethodGet, "/v2/microvms/"+url.PathEscape(id), nil, "")
	if err != nil {
		return Machine{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Machine{}, m.apiErr(res, "")
	}
	vm, err := decodeVM(res.Body)
	if err != nil {
		return Machine{}, err
	}
	return vm.machine(m.insecure), nil
}

func (m *MicroVM) action(ctx context.Context, id, name string) (Machine, error) {
	if strings.TrimSpace(id) == "" || id == "pending" {
		return Machine{}, errors.New("missing machine id")
	}
	m.forget(id)
	res, err := m.do(ctx, http.MethodPost, "/v2/microvms/"+url.PathEscape(id)+"/"+name, nil, "")
	if err != nil {
		return Machine{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusAccepted {
		return Machine{}, m.apiErr(res, "")
	}
	vm, err := decodeVM(res.Body)
	if err != nil {
		return Machine{}, err
	}
	out := vm.machine(m.insecure)
	m.remember(out)
	return out, nil
}

func (m *MicroVM) do(ctx context.Context, method, path string, body []byte, secret string) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := m.http.Do(req)
	if err != nil {
		return nil, errors.New(redact(err.Error(), secret, m.token))
	}
	return res, nil
}

func (m *MicroVM) apiErr(res *http.Response, secret string) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	msg = redact(msg, secret, m.token)
	msg = strings.Join(strings.Fields(msg), " ")
	if msg == "" {
		return fmt.Errorf("machine api status %d", res.StatusCode)
	}
	return fmt.Errorf("machine api status %d: %s", res.StatusCode, msg)
}

func (m *MicroVM) edgeOnce(ctx context.Context, method, target, agentToken string, body []byte) (int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return 0, err
	}
	m.edgeAuth(req, agentToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := m.edge.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, nil
}

func (m *MicroVM) edgeAuth(req *http.Request, agentToken string) {
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("X-Cast-Authorization", "Bearer "+agentToken)
}

func decodeVM(r io.Reader) (apiVM, error) {
	var body struct {
		MicroVM apiVM `json:"microvm"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&body); err != nil {
		return apiVM{}, err
	}
	return body.MicroVM, nil
}

type createReq struct {
	Name      string            `json:"name"`
	Region    string            `json:"region"`
	Size      sizeReq           `json:"size"`
	Source    sourceReq         `json:"source"`
	HTTPPort  int               `json:"http_port"`
	AutoPause autoReq           `json:"auto_pause"`
	Tags      []string          `json:"tags"`
	Env       map[string]string `json:"environment"`
}

type sizeReq struct {
	CPU    int `json:"cpu"`
	Memory int `json:"memory"`
}

type sourceReq struct {
	OCIRef string `json:"oci_ref"`
}

type autoReq struct {
	Idle string `json:"idle_timeout"`
}

type apiVM struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Region  string   `json:"region"`
	Failure string   `json:"failure_reason"`
	Tags    []string `json:"tags"`
	Size    struct {
		CPU    int `json:"cpu"`
		Memory int `json:"memory"`
	} `json:"size"`
	Source struct {
		OCI string `json:"oci_ref"`
	} `json:"source"`
	URLs []apiURL `json:"urls"`
}

type apiURL struct {
	Default  bool   `json:"default"`
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
}

func (v apiVM) machine(insecure bool) Machine {
	ep, rejected := classifyAddress(v.URLs, insecure)
	return Machine{
		ID:              v.ID,
		Name:            v.Name,
		State:           v.State,
		Failure:         v.Failure,
		Endpoint:        ep,
		AddressRejected: rejected,
		Image:           v.Source.OCI,
		Region:          v.Region,
		CPU:             v.Size.CPU,
		Memory:          v.Size.Memory,
		Tags:            append([]string(nil), v.Tags...),
	}
}

// classifyAddress separates a hostname that is not published yet from one
// that was published and then failed the edge check.
func classifyAddress(urls []apiURL, insecure bool) (endpoint string, rejected bool) {
	raw, status, ok := chooseURL(urls)
	if !ok || strings.EqualFold(status, "PENDING") || strings.TrimSpace(raw) == "" {
		return "", false
	}
	ep := endpointFromHost(raw, insecure)
	if ep == "" {
		return "", true
	}
	return ep, false
}

func chooseURL(urls []apiURL) (hostname, status string, ok bool) {
	var picked apiURL
	found := false
	for _, u := range urls {
		if !found || u.Default {
			picked = u
			found = true
		}
		if u.Default {
			break
		}
	}
	if !found {
		return "", "", false
	}
	return picked.Hostname, picked.Status, true
}

func endpoint(urls []apiURL, insecure bool) string {
	ep, _ := classifyAddress(urls, insecure)
	return ep
}

func endpointFromHost(raw string, insecure bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || !edgeAllowed(u, insecure) {
			return ""
		}
		return strings.TrimRight(raw, "/")
	}
	if insecure {
		return "http://" + raw
	}
	if !edgeSuffix(raw) {
		return ""
	}
	return "https://" + raw
}

func edgeSuffix(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "microvm.ondigitalocean.com" || strings.HasSuffix(host, ".microvm.ondigitalocean.com")
}

func edgeAllowed(u *url.URL, insecure bool) bool {
	if u == nil || u.Hostname() == "" {
		return false
	}
	if insecure {
		return u.Scheme == "http" || u.Scheme == "https"
	}
	return u.Scheme == "https" && edgeSuffix(u.Hostname())
}

func validateSpec(spec CreateSpec) error {
	if spec.Name == "" || spec.AgentID == "" || spec.Image == "" || spec.Bootstrap == "" {
		return errors.New("machine spec is incomplete")
	}
	if strings.ContainsAny(spec.Name, " /") || spec.Name != strings.ToLower(spec.Name) {
		return errors.New("machine name is invalid")
	}
	u, err := url.Parse(spec.ControlURL)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "https") {
		return errors.New("control plane URL must use https")
	}
	if spec.Region == "" {
		return errors.New("machine region is empty")
	}
	if spec.CPU != DefaultCPU || spec.MemoryMiB != DefaultMemoryMiB {
		return errors.New("machine size must be 1:2048")
	}
	if spec.HTTPPort != DefaultHTTPPort {
		return errors.New("machine http port must be 8080")
	}
	return nil
}

func formatIdle(d time.Duration) string {
	if d <= 0 {
		d = DefaultIdle
	}
	if d > 4*time.Hour {
		d = 4 * time.Hour
	}
	d = d.Truncate(time.Second)
	if d >= time.Hour && d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

func redact(msg, secret, token string) string {
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "[redacted]")
	}
	return msg
}

func (m *MicroVM) checkEdge(endpoint, agentToken string) error {
	if strings.TrimSpace(agentToken) == "" {
		return errors.New("agent token is empty")
	}
	u, err := url.Parse(endpoint)
	if err != nil || !edgeAllowed(u, m.insecure) {
		return errors.New("machine endpoint is invalid")
	}
	return nil
}

func (m *MicroVM) cached(id string) (Machine, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hit, ok := m.cache[id]
	if !ok || time.Since(hit.at) >= 5*time.Second {
		return Machine{}, false
	}
	return hit.vm, true
}

func (m *MicroVM) remember(vm Machine) {
	if vm.ID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cache == nil {
		m.cache = map[string]statusHit{}
	}
	m.cache[vm.ID] = statusHit{at: time.Now(), vm: vm}
}

func (m *MicroVM) forget(id string) {
	m.mu.Lock()
	delete(m.cache, id)
	m.mu.Unlock()
}

func machineTags(spec CreateSpec) []string {
	tags := []string{"castbot", "agent:" + spec.AgentID}
	seen := map[string]bool{"castbot": true, "agent:" + spec.AgentID: true}
	for _, tag := range spec.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] || strings.ContainsAny(tag, " ,") {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	return tags
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return "", errors.New("file name is invalid")
	}
	name = strings.Trim(name, "/")
	if name == "" || strings.Contains(name, "//") {
		return "", errors.New("file name is invalid")
	}
	return name, nil
}
