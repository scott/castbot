package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	UserAgent          = "castbot-update"
	defaultReleaseBase = "https://github.com/scott/castbot/releases/latest/download"
	defaultCompareBase = "https://api.github.com/repos/scott/castbot/compare/"
	maxMeta            = 1 << 20
	maxCompare         = 8 << 20
)

// Checker fetches a release and decides whether it can be offered.
// Tests set the bases and Client. A nil Client uses the public release hosts.
type Checker struct {
	Client       *http.Client
	ReleaseBase  string
	CompareBase  string
	RegistryBase string
	Key          []byte
	Now          func() time.Time
	// Visible overrides the public registry lookup.
	Visible func(context.Context, string) (bool, error)
}

// State is the running process and the mode already resolved for this pass.
type State struct {
	Version   string
	Commit    string
	Schema    int
	Image     string
	Mode      string
	CachePath string
}

// Fetched is a signature-checked release manifest.
type Fetched struct {
	Manifest Manifest
	Sums     []byte
}

// Check fetches the latest release and decides what to record.
// An error means the check is incomplete: the caller keeps the previous cache.
// A Cache with Available false is a finished check that should be stored.
func (c *Checker) Check(ctx context.Context, st State) (Cache, error) {
	fetched, err := c.Fetch(ctx)
	if err != nil {
		return Cache{}, err
	}
	m := fetched.Manifest
	if err := m.Validate(); err != nil {
		return Cache{}, err
	}
	out := Cache{CheckedAt: c.now().UTC().Format(time.RFC3339)}
	if !Newer(st.Version, m.Version) || m.Schema < st.Schema {
		return out, nil
	}
	status, err := c.ancestor(ctx, st.Commit, m.Commit)
	if err != nil {
		return Cache{}, err
	}
	if status != "ahead" && status != "identical" {
		return out, nil
	}
	plan, err := DecideImage(ctx, st.Image, m.Commit, m.RuntimeImage, m.RuntimeImageCommit, c.imageVisible)
	if err != nil {
		return Cache{}, err
	}
	out.Version = m.Version
	out.Commit = m.Commit
	out.Notes = m.Notes
	if plan.Block {
		out.ImageBlocked = true
		return out, nil
	}
	out.Available = true
	out.ImageMoves = plan.Move
	return out, nil
}

// Fetch downloads the sums, the signature, and release.json.
// The signature is checked with Key, or the compiled-in key when Key is empty.
func (c *Checker) Fetch(ctx context.Context) (Fetched, error) {
	base := strings.TrimRight(c.ReleaseBase, "/")
	if base == "" {
		base = defaultReleaseBase
	}
	sums, err := c.get(ctx, base+"/SHA256SUMS", "application/octet-stream", maxMeta)
	if err != nil {
		return Fetched{}, errors.New("release download failed")
	}
	sig, err := c.get(ctx, base+"/SHA256SUMS.minisig", "application/octet-stream", maxMeta)
	if err != nil {
		return Fetched{}, errors.New("release download failed")
	}
	body, err := c.get(ctx, base+"/release.json", "application/octet-stream", maxMeta)
	if err != nil {
		return Fetched{}, errors.New("release download failed")
	}
	key := c.Key
	if len(key) == 0 {
		key = PublicKey()
	}
	if err := Verify(key, sums, sig); err != nil {
		return Fetched{}, errors.New("release signature was rejected")
	}
	if err := sumsCover(sums, map[string][]byte{"release.json": body}); err != nil {
		return Fetched{}, err
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Fetched{}, errors.New("release manifest is unexpected")
	}
	return Fetched{Manifest: m, Sums: append([]byte(nil), sums...)}, nil
}

func (c *Checker) ancestor(ctx context.Context, running, release string) (string, error) {
	if !shaRE.MatchString(running) || !shaRE.MatchString(release) {
		return "", errors.New("compare failed")
	}
	if running == release {
		return "identical", nil
	}
	base := c.CompareBase
	if base == "" {
		base = defaultCompareBase
	}
	body, err := c.get(ctx, base+running+"..."+release, "application/vnd.github+json", maxCompare)
	if err != nil {
		return "", errors.New("compare failed")
	}
	var payload struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Status == "" {
		return "", errors.New("compare failed")
	}
	return payload.Status, nil
}

func (c *Checker) get(ctx context.Context, raw, accept string, limit int64) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("release url is unexpected")
	}
	if c.Client == nil {
		if u.Scheme != "https" || !allowedReleaseHost(u.Hostname()) {
			return nil, errors.New("release host is unexpected")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("release url is unexpected")
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", accept)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, errors.New("download failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errors.New("download failed")
	}
	if resp.StatusCode != http.StatusOK || int64(len(body)) == 0 || int64(len(body)) > limit {
		return nil, errors.New("download failed")
	}
	return body, nil
}

func (c *Checker) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return defaultHTTP()
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func defaultHTTP() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("release redirect is unexpected")
			}
			if req.URL.Scheme != "https" || !allowedReleaseHost(req.URL.Hostname()) {
				return errors.New("release host is unexpected")
			}
			return nil
		},
	}
}

func allowedReleaseHost(host string) bool {
	switch host {
	case "github.com", "api.github.com", "ghcr.io",
		"objects.githubusercontent.com",
		"release-assets.githubusercontent.com",
		"github-releases.githubusercontent.com":
		return true
	default:
		return strings.HasSuffix(host, ".githubusercontent.com")
	}
}

// sumsCover checks that every named body matches SHA256SUMS and that both
// binary names are present. The binaries themselves are not downloaded here.
func sumsCover(sums []byte, bodies map[string][]byte) error {
	lines := strings.Split(strings.TrimSuffix(string(sums), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return errors.New("sums are empty")
	}
	seen := map[string]string{}
	for _, line := range lines {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || !sumRE.MatchString(sum) || !fileNameOK(name) {
			return errors.New("sum line is unexpected")
		}
		if _, dup := seen[name]; dup {
			return errors.New("sum line is unexpected")
		}
		seen[name] = sum
	}
	for _, name := range []string{"release.json", "castbot_linux_amd64", "castbot_linux_arm64"} {
		if !sumRE.MatchString(seen[name]) {
			return errors.New("sum line is unexpected")
		}
	}
	for name, body := range bodies {
		sum := sha256.Sum256(body)
		if seen[name] != hex.EncodeToString(sum[:]) {
			return errors.New("sum does not match")
		}
	}
	return nil
}
