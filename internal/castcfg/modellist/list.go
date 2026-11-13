// Package modellist loads one provider's model ids.
//
// The token is sent only as a request header. It is never written to a query
// string and this package does not log it. A failed fetch is not stored, so
// an empty or broken response cannot stick as the catalog.
package modellist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/scott/castbot/internal/castcfg"
)

const (
	defaultTTL     = 3 * time.Minute
	defaultTimeout = 8 * time.Second
	maxBody        = 1 << 20
)

var (
	// ErrUnknown means the manifest has no such provider, or it is reserved.
	ErrUnknown = errors.New("unknown provider")
	// ErrNotConfigured means this caller has no key, or ollama has no base URL.
	ErrNotConfigured = errors.New("provider is not configured")
	// ErrUnreachable means ollama's base URL did not answer /models.
	ErrUnreachable = errors.New("provider did not answer")

	errSkip = errors.New("skip")
)

// Model is one choice. ID is the prefixed id a run stores. Label is the
// id without that prefix. Nothing else from the upstream body is kept.
type Model struct {
	ID    string
	Label string
}

// List is a provider's models. Fallback is true when the ids came from the
// manifest because the live list was not used.
type List struct {
	Models   []Model
	Fallback bool
}

// Catalog caches live lists in memory. The cache key is the provider plus a
// hash of the token, so two users with different keys do not share a list.
type Catalog struct {
	mu      sync.Mutex
	client  *http.Client
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time
	bases   map[string]string
	cache   map[string]cached
	offline bool
}

type cached struct {
	list  List
	until time.Time
}

// New is a catalog that can call providers. Tests swap the client and the
// base URL so a suite never dials a real host.
func New() *Catalog {
	return &Catalog{
		client: &http.Client{
			Timeout: defaultTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		ttl:     defaultTTL,
		timeout: defaultTimeout,
		now:     time.Now,
		cache:   map[string]cached{},
	}
}

// SetClient replaces the HTTP client. A nil client is ignored.
func (c *Catalog) SetClient(client *http.Client) {
	if client == nil {
		return
	}
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
}

// SetBase points one provider at a stand-in host. Production leaves this empty
// and uses the manifest base URL.
func (c *Catalog) SetBase(provider, base string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bases == nil {
		c.bases = map[string]string{}
	}
	c.bases[provider] = strings.TrimRight(strings.TrimSpace(base), "/")
}

// SetTimeout caps one upstream call. Tests use a short cap.
func (c *Catalog) SetTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.timeout = d
	if c.client != nil {
		c.client.Timeout = d
	}
	c.mu.Unlock()
}

// SetTTL is how long a successful live list is kept.
func (c *Catalog) SetTTL(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.ttl = d
	c.mu.Unlock()
}

// SetNow replaces the clock. Tests expire a cache entry without sleeping.
func (c *Catalog) SetNow(now func() time.Time) {
	if now == nil {
		return
	}
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

// SetOffline makes List return the manifest ids and never dial.
func (c *Catalog) SetOffline() {
	c.mu.Lock()
	c.offline = true
	c.mu.Unlock()
}

// AllowsFetch reports whether List may dial. Snapshot never dials.
func (c *Catalog) AllowsFetch() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.offline
}

// HasLive reports a warm live list for this token. A cold cache is a miss.
// This does not dial.
func (c *Catalog) HasLive(provider, token string) bool {
	list, ok := c.cached(provider, token)
	return ok && !list.Fallback
}

// DefaultID is the manifest default for a provider, already prefixed.
func DefaultID(provider string) string {
	e, ok := findEntry(provider)
	if !ok || e.Impl == "reserved" {
		return ""
	}
	list := offlineList(e)
	if len(list.Models) == 0 {
		return ""
	}
	return list.Models[0].ID
}

// OllamaConfigured reports whether a base URL was set for ollama.
// The keyless flag in the manifest does not count as configured.
func OllamaConfigured() bool {
	e, ok := findEntry("ollama")
	if !ok {
		return false
	}
	_, ok = configuredBase(e)
	return ok
}

// Snapshot is the list for a page render. It returns a warm cache entry or
// the manifest ids. It does not dial.
func (c *Catalog) Snapshot(provider, token string) List {
	if list, ok := c.cached(provider, token); ok {
		return list
	}
	e, ok := findEntry(provider)
	if !ok || e.Impl == "reserved" {
		return List{}
	}
	return offlineList(e)
}

// CachedID reports whether id is in the warm live list for this token.
// A cold cache is a miss. This does not dial.
func (c *Catalog) CachedID(provider, token, id string) bool {
	list, ok := c.cached(provider, token)
	if !ok || list.Fallback {
		return false
	}
	id = strings.TrimSpace(id)
	for _, model := range list.Models {
		if model.ID == id {
			return true
		}
	}
	return false
}

// List returns one provider's models. An empty token is not configured, except
// ollama, which needs a reachable base URL instead of a key. A failed fetch
// returns manifest ids and is not cached. Ollama returns ErrUnreachable
// instead of those ids, because a keyless default is not a configured host.
func (c *Catalog) List(ctx context.Context, provider, token string) (List, error) {
	e, ok := findEntry(provider)
	if !ok || e.Impl == "reserved" {
		return List{}, ErrUnknown
	}
	token = strings.TrimSpace(token)
	if e.Key == "ollama" {
		return c.listOllama(ctx, e, token)
	}
	if token == "" {
		return List{}, ErrNotConfigured
	}
	if list, ok := c.cached(e.Key, token); ok {
		return list, nil
	}
	if c.isOffline() {
		return offlineList(e), nil
	}
	ids, live := c.fetch(ctx, e, token)
	if !live {
		return offlineList(e), nil
	}
	list := makeList(e, ids, false)
	c.store(e.Key, token, list)
	return list, nil
}

func (c *Catalog) listOllama(ctx context.Context, e entry, token string) (List, error) {
	if _, ok := configuredBase(e); !ok {
		return List{}, ErrNotConfigured
	}
	if list, ok := c.cached(e.Key, token); ok {
		return list, nil
	}
	if c.isOffline() {
		return List{}, ErrUnreachable
	}
	ids, live := c.fetch(ctx, e, token)
	if !live {
		return List{}, ErrUnreachable
	}
	list := makeList(e, ids, false)
	c.store(e.Key, token, list)
	return list, nil
}

func (c *Catalog) isOffline() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offline
}

func (c *Catalog) fetch(ctx context.Context, e entry, token string) ([]string, bool) {
	if e.Key == "openrouter" {
		return c.fetchOpenRouter(ctx, e, token)
	}
	if strings.TrimSpace(e.ModelsPath) == "" {
		return nil, false
	}
	root := c.root(e)
	path := e.ModelsPath
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	plain := root + path
	headers := modelHeaders(e, token)
	var status int
	var body []byte
	var err error
	if e.ModelsLifecycle != "" {
		body, status, err = c.get(ctx, withQuery(plain, "lifecycle", e.ModelsLifecycle), headers, token)
		if err == nil && (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) {
			body, status, err = c.get(ctx, plain, headers, token)
		}
	} else {
		body, status, err = c.get(ctx, plain, headers, token)
	}
	if err != nil || status != http.StatusOK {
		return nil, false
	}
	ids := filterIDs(e, decodeIDs(body))
	// An empty chat list is a real answer only for ollama, which accepts
	// whatever is pulled locally. Every other provider falls back to the
	// manifest ids. acceptsAnyModel does not open a freeform text box.
	if len(ids) == 0 && !(e.Key == "ollama" && e.AcceptsAnyModel) {
		return nil, false
	}
	return ids, true
}

func (c *Catalog) fetchOpenRouter(ctx context.Context, e entry, token string) ([]string, bool) {
	root := c.root(e)
	headers := modelHeaders(e, token)
	body, status, err := c.get(ctx, root+"/models/user", headers, token)
	if err == nil && status == http.StatusOK {
		ids := filterIDs(e, decodeIDs(body))
		if len(ids) > 0 {
			return ids, true
		}
		return nil, false
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		body, status, err = c.get(ctx, root+"/models", nil, "")
		if err == nil && status == http.StatusOK {
			ids := filterIDs(e, decodeIDs(body))
			if len(ids) > 0 {
				return ids, true
			}
		}
	}
	return nil, false
}

func (c *Catalog) get(ctx context.Context, raw string, headers http.Header, token string) ([]byte, int, error) {
	if secretInURL(raw, token, headers) {
		return nil, 0, errSkip
	}
	timeout := c.timeoutOr()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, 0, errSkip
	}
	for key, vals := range headers {
		for _, v := range vals {
			req.Header.Add(key, v)
		}
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, errSkip
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return nil, resp.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, 0, errSkip
	}
	return body, resp.StatusCode, nil
}

func (c *Catalog) httpClient() *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}

func (c *Catalog) timeoutOr() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timeout <= 0 {
		return defaultTimeout
	}
	return c.timeout
}

func (c *Catalog) root(e entry) string {
	c.mu.Lock()
	base := ""
	if c.bases != nil {
		base = c.bases[e.Key]
	}
	c.mu.Unlock()
	if base == "" && e.Key == "ollama" {
		if v, ok := configuredBase(e); ok {
			base = v
		}
	}
	if base == "" {
		base = e.BaseURL
	}
	return modelListRoot(e, base)
}

func secretInURL(raw, token string, headers http.Header) bool {
	if token != "" && strings.Contains(raw, token) {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	if u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return true
	}
	q := u.Query()
	for _, key := range []string{"api_key", "apikey", "key", "token", "access_token"} {
		if q.Get(key) != "" {
			return true
		}
	}
	for _, vals := range headers {
		for _, v := range vals {
			if v != "" && strings.Contains(raw, v) {
				return true
			}
		}
	}
	return false
}

func modelHeaders(e entry, token string) http.Header {
	h := http.Header{}
	if len(e.ModelsHeaders) > 0 {
		for name, value := range e.ModelsHeaders {
			rendered := value
			if strings.Contains(value, "${token}") {
				if token == "" {
					continue
				}
				rendered = strings.ReplaceAll(value, "${token}", token)
			}
			if strings.TrimSpace(rendered) == "" {
				continue
			}
			h.Set(name, rendered)
		}
		return h
	}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return h
}

func modelListRoot(e entry, base string) string {
	root := strings.TrimRight(strings.TrimSpace(base), "/")
	if e.NormalizeV1Base && !strings.HasSuffix(root, "/v1") {
		root += "/v1"
	}
	return root
}

func withQuery(raw, key, value string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

type upstreamRow struct {
	ID        string `json:"id"`
	Lifecycle string `json:"lifecycle"`
}

func decodeIDs(body []byte) []upstreamRow {
	var page struct {
		Data []upstreamRow `json:"data"`
	}
	if json.Unmarshal(body, &page) != nil {
		return nil
	}
	return page.Data
}

func filterIDs(e entry, rows []upstreamRow) []string {
	prefix := firstPrefix(e)
	seen := map[string]bool{}
	var out []string
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			continue
		}
		if e.ModelsLifecycle != "" && row.Lifecycle != "" && row.Lifecycle != e.ModelsLifecycle {
			continue
		}
		if e.StripModelIdPrefix != "" && strings.HasPrefix(id, e.StripModelIdPrefix) {
			id = strings.TrimPrefix(id, e.StripModelIdPrefix)
		}
		if e.filter != nil && !e.filter.MatchString(id) {
			continue
		}
		full := withPrefix(prefix, id)
		if full == "" || seen[full] {
			continue
		}
		seen[full] = true
		out = append(out, full)
	}
	return out
}

func offlineList(e entry) List {
	prefix := firstPrefix(e)
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		id = withPrefix(prefix, strings.TrimSpace(id))
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	add(e.DefaultModel)
	for _, name := range e.KnownModels {
		add(name)
	}
	for _, name := range e.FallbackModels {
		add(name)
	}
	add(e.LLMDefault)
	return makeList(e, ids, true)
}

func makeList(e entry, ids []string, fallback bool) List {
	prefix := firstPrefix(e)
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, Model{ID: id, Label: labelOf(prefix, id)})
	}
	return List{Models: out, Fallback: fallback}
}

func labelOf(prefix, id string) string {
	if prefix != "" && strings.HasPrefix(id, prefix) {
		rest := strings.TrimPrefix(id, prefix)
		if rest != "" {
			return rest
		}
	}
	return id
}

func withPrefix(prefix, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if prefix == "" || strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

func firstPrefix(e entry) string {
	if len(e.Prefixes) == 0 {
		return ""
	}
	return e.Prefixes[0]
}

func configuredBase(e entry) (string, bool) {
	name := strings.TrimSpace(e.BaseURLEnv)
	if name == "" {
		return "", false
	}
	v := strings.TrimRight(strings.TrimSpace(os.Getenv(name)), "/")
	if v == "" {
		return "", false
	}
	return v, true
}

func (c *Catalog) cached(provider, token string) (List, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := c.cache[cacheKey(provider, token)]
	if !ok || !c.now().Before(hit.until) {
		return List{}, false
	}
	return cloneList(hit.list), true
}

func (c *Catalog) store(provider, token string, list List) {
	if list.Fallback {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	c.cache[cacheKey(provider, token)] = cached{
		list:  cloneList(list),
		until: c.now().Add(c.ttl),
	}
}

func cloneList(list List) List {
	out := List{Fallback: list.Fallback, Models: make([]Model, len(list.Models))}
	copy(out.Models, list.Models)
	return out
}

func cacheKey(provider, token string) string {
	sum := sha256.Sum256([]byte(token))
	return provider + ":" + hex.EncodeToString(sum[:])
}

type entry struct {
	Key                string            `json:"key"`
	Impl               string            `json:"impl"`
	Prefixes           []string          `json:"prefixes"`
	DefaultModel       string            `json:"defaultModel"`
	KnownModels        []string          `json:"knownModels"`
	FallbackModels     []string          `json:"fallbackModels"`
	LLMDefault         string            `json:"llmDefaultModel"`
	BaseURL            string            `json:"baseUrl"`
	BaseURLEnv         string            `json:"baseUrlEnv"`
	Keyless            bool              `json:"keyless"`
	AcceptsAnyModel    bool              `json:"acceptsAnyModel"`
	ModelsPath         string            `json:"modelsPath"`
	ModelsFilter       string            `json:"modelsFilter"`
	ModelsHeaders      map[string]string `json:"modelsHeaders"`
	ModelsLifecycle    string            `json:"modelsLifecycle"`
	StripModelIdPrefix string            `json:"stripModelIdPrefix"`
	NormalizeV1Base    bool              `json:"normalizeV1Base"`
	filter             *modelFilter
}

// modelFilter runs the manifest pattern. Those patterns use lookaheads, which
// the standard library does not implement.
type modelFilter struct {
	re *regexp2.Regexp
}

func (f *modelFilter) MatchString(s string) bool {
	if f == nil || f.re == nil {
		return false
	}
	ok, err := f.re.MatchString(s)
	return err == nil && ok
}

var (
	entryOnce sync.Once
	entryList []entry
	entryErr  error
)

func allEntries() ([]entry, error) {
	entryOnce.Do(func() {
		if err := json.Unmarshal(castcfg.ManifestJSON(), &entryList); err != nil {
			entryErr = err
			return
		}
		for i := range entryList {
			if entryList[i].ModelsFilter == "" {
				continue
			}
			re, err := regexp2.Compile(entryList[i].ModelsFilter, regexp2.None)
			if err != nil {
				entryErr = err
				return
			}
			re.MatchTimeout = time.Second
			entryList[i].filter = &modelFilter{re: re}
		}
	})
	return entryList, entryErr
}

func findEntry(key string) (entry, bool) {
	list, err := allEntries()
	if err != nil {
		return entry{}, false
	}
	key = strings.TrimSpace(key)
	for _, e := range list {
		if e.Key == key {
			return e, true
		}
	}
	return entry{}, false
}
