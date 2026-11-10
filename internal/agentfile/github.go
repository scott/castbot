// Package agentfile reads a Cast agent folder from an upload or a public
// github.com URL. It never runs anything from the folder.
package agentfile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Repo is a public GitHub repository the user named.
// Unresolved is the path after /tree/ when the ref may contain a slash.
// Fetch resolves it against the public refs API.
type Repo struct {
	Owner      string
	Name       string
	Ref        string
	Subpath    string
	Unresolved []string
}

// ParseGitHub accepts an https URL whose host is github.com.
// A userinfo part, another host, or a non-https scheme is rejected.
// Ref and subpath are optional. Private repositories are not represented here.
func ParseGitHub(raw string) (Repo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\\") {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	if u.User != nil {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	if !strings.EqualFold(u.Hostname(), "github.com") {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	if port := u.Port(); port != "" && port != "443" {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	path := strings.Trim(u.EscapedPath(), "/")
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || !gitName(parts[0]) || !gitName(strings.TrimSuffix(parts[1], ".git")) {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	repo := Repo{Owner: parts[0], Name: strings.TrimSuffix(parts[1], ".git")}
	ref := strings.TrimSpace(u.Query().Get("ref"))
	rest := parts[2:]
	if len(rest) == 0 {
		if !refOK(ref) {
			return Repo{}, errors.New("repository URL is not a public github.com URL")
		}
		repo.Ref = ref
		return repo, nil
	}
	if rest[0] != "tree" || len(rest) < 2 {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	tree := rest[1:]
	if ref != "" {
		sub, ok := splitRef(tree, ref)
		if !ok || !refOK(ref) || !subpathOK(sub) {
			return Repo{}, errors.New("repository URL is not a public github.com URL")
		}
		repo.Ref = ref
		repo.Subpath = sub
		return repo, nil
	}
	if len(tree) == 1 {
		if !refOK(tree[0]) {
			return Repo{}, errors.New("repository URL is not a public github.com URL")
		}
		repo.Ref = tree[0]
		return repo, nil
	}
	for _, part := range tree {
		if !refOK(part) && !subpathOK(part) {
			return Repo{}, errors.New("repository URL is not a public github.com URL")
		}
	}
	repo.Unresolved = tree
	return repo, nil
}

// splitRef takes the segments after /tree/ and a known ref. The ref may
// contain slashes, so it can cover more than one segment.
func splitRef(tree []string, ref string) (string, bool) {
	joined := strings.Join(tree, "/")
	if joined == ref {
		return "", true
	}
	prefix := ref + "/"
	if strings.HasPrefix(joined, prefix) {
		return strings.TrimPrefix(joined, prefix), true
	}
	return "", false
}

func gitName(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 100 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}

func refOK(ref string) bool {
	if ref == "" {
		return true
	}
	if len(ref) > 200 || strings.Contains(ref, "..") || strings.HasPrefix(ref, "/") {
		return false
	}
	for _, c := range ref {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.' || c == '/':
		default:
			return false
		}
	}
	return true
}

func subpathOK(sub string) bool {
	if sub == "" {
		return true
	}
	if len(sub) > 300 || strings.Contains(sub, "..") || strings.HasPrefix(sub, "/") {
		return false
	}
	for _, c := range sub {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.' || c == '/':
		default:
			return false
		}
	}
	return true
}

// Fetch downloads the repository archive from base and returns the repo
// it actually used. An empty base uses codeload.github.com. The client
// should refuse redirects to other hosts. The body is limited to maxArchive
// bytes. A /tree/<branch>/<sub> URL tries that branch on the archive host
// before it calls the refs API.
func Fetch(ctx context.Context, client *http.Client, base string, repo Repo) ([]byte, Repo, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
	}
	if !gitName(repo.Owner) || !gitName(repo.Name) || !refOK(repo.Ref) {
		return nil, Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	if len(repo.Unresolved) > 1 && refOK(repo.Unresolved[0]) && subpathOK(strings.Join(repo.Unresolved[1:], "/")) {
		trial := repo
		trial.Ref = repo.Unresolved[0]
		trial.Subpath = strings.Join(repo.Unresolved[1:], "/")
		trial.Unresolved = nil
		body, err := download(ctx, client, base, trial, false)
		if err == nil {
			if _, rerr := ReadArchive(body, trial.Subpath); rerr == nil {
				return body, trial, nil
			} else if !IsNoAgent(rerr) {
				return nil, Repo{}, rerr
			}
		} else if IsRateLimit(err) || IsPrivate(err) || IsOversize(err) {
			return nil, Repo{}, err
		} else if !IsMissing(err) {
			return nil, Repo{}, err
		}
	}
	if len(repo.Unresolved) > 0 {
		resolved, err := resolveRef(ctx, client, base, repo)
		if err != nil {
			return nil, Repo{}, err
		}
		repo = resolved
	}
	body, err := download(ctx, client, base, repo, true)
	return body, repo, err
}

func download(ctx context.Context, client *http.Client, base string, repo Repo, missingPrivate bool) ([]byte, error) {
	ref := repo.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if base == "" {
		base = "https://codeload.github.com"
	}
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("repository archive is unavailable")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + repo.Owner + "/" + repo.Name + "/tar.gz/" + ref
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: githubRedirect}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("repository archive is unavailable")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxArchive+1))
	if err != nil {
		return nil, errors.New("repository archive is unavailable")
	}
	if rateLimited(res, body) {
		return nil, errRateLimit
	}
	if res.StatusCode == http.StatusNotFound {
		if missingPrivate {
			return nil, errPrivate
		}
		return nil, errMissing
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, errPrivate
	}
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("repository archive is unavailable")
	}
	if len(body) > maxArchive {
		return nil, errOversize
	}
	return body, nil
}

// ErrPrivate means the archive was not public.
var errPrivate = errors.New("private")

var errMissing = errors.New("missing")

var errRateLimit = errors.New("rate limit")

func IsPrivate(err error) bool { return errors.Is(err, errPrivate) }

func IsMissing(err error) bool { return errors.Is(err, errMissing) }

func IsRateLimit(err error) bool { return errors.Is(err, errRateLimit) }

func rateLimited(res *http.Response, body []byte) bool {
	if res.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if res.StatusCode != http.StatusForbidden {
		return false
	}
	if res.Header.Get("X-RateLimit-Remaining") == "0" || res.Header.Get("Retry-After") != "" {
		return true
	}
	return strings.Contains(strings.ToLower(string(body)), "rate limit")
}

func githubRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("redirect left github")
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return errors.New("redirect left github")
	}
	switch strings.ToLower(req.URL.Hostname()) {
	case "github.com", "codeload.github.com", "api.github.com":
		if req.URL.User != nil {
			return errors.New("redirect left github")
		}
		return nil
	default:
		return errors.New("redirect left github")
	}
}

// resolveRef picks the longest public branch or tag that matches the
// segments after /tree/. The remainder is the subpath. A miss asks for ?ref=.
func resolveRef(ctx context.Context, client *http.Client, base string, repo Repo) (Repo, error) {
	if len(repo.Unresolved) == 0 {
		return repo, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: githubRedirect}
	}
	api := "https://api.github.com"
	if base != "" && !strings.Contains(strings.ToLower(base), "codeload.github.com") {
		api = strings.TrimRight(base, "/")
	}
	joined := strings.Join(repo.Unresolved, "/")
	names, err := matchingRefs(ctx, client, api, repo, repo.Unresolved[0])
	if err != nil {
		return Repo{}, err
	}
	best := ""
	for _, name := range names {
		if name == joined || strings.HasPrefix(joined, name+"/") {
			if len(name) > len(best) {
				best = name
			}
		}
	}
	if best == "" {
		return Repo{}, errors.New("a branch name with a slash needs ?ref=")
	}
	sub, ok := splitRef(repo.Unresolved, best)
	if !ok || !refOK(best) || !subpathOK(sub) {
		return Repo{}, errors.New("a branch name with a slash needs ?ref=")
	}
	repo.Ref = best
	repo.Subpath = sub
	repo.Unresolved = nil
	return repo, nil
}

type refItem struct {
	names []string
	until time.Time
}

var (
	refMu    sync.Mutex
	refItems map[string]refItem
)

func matchingRefs(ctx context.Context, client *http.Client, api string, repo Repo, prefix string) ([]string, error) {
	key := api + "|" + repo.Owner + "/" + repo.Name + "|" + prefix
	refMu.Lock()
	if item, ok := refItems[key]; ok && time.Now().Before(item.until) {
		names := append([]string(nil), item.names...)
		refMu.Unlock()
		return names, nil
	}
	refMu.Unlock()
	names, err := loadMatchingRefs(ctx, client, api, repo, prefix)
	if err != nil {
		return nil, err
	}
	refMu.Lock()
	if refItems == nil {
		refItems = map[string]refItem{}
	}
	refItems[key] = refItem{names: append([]string(nil), names...), until: time.Now().Add(time.Minute)}
	refMu.Unlock()
	return names, nil
}

func loadMatchingRefs(ctx context.Context, client *http.Client, api string, repo Repo, prefix string) ([]string, error) {
	var names []string
	for _, kind := range []string{"heads", "tags"} {
		u := api + "/repos/" + repo.Owner + "/" + repo.Name + "/git/matching-refs/" + kind + "/" + prefix
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, errors.New("repository archive is unavailable")
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		res, err := client.Do(req)
		if err != nil {
			return nil, errors.New("repository archive is unavailable")
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if err != nil {
			return nil, errors.New("repository archive is unavailable")
		}
		if rateLimited(res, body) {
			return nil, errRateLimit
		}
		if res.StatusCode == http.StatusNotFound {
			continue
		}
		if res.StatusCode != http.StatusOK {
			return nil, errors.New("a branch name with a slash needs ?ref=")
		}
		var list []struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, errors.New("a branch name with a slash needs ?ref=")
		}
		needle := "refs/" + kind + "/"
		for _, item := range list {
			name := strings.TrimPrefix(item.Ref, needle)
			if name != "" && name != item.Ref {
				names = append(names, name)
			}
		}
	}
	return names, nil
}
