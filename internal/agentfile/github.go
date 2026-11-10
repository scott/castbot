// Package agentfile reads a Cast agent folder from an upload or a public
// github.com URL. It never runs anything from the folder.
package agentfile

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Repo is a public GitHub repository the user named.
type Repo struct {
	Owner   string
	Name    string
	Ref     string
	Subpath string
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
	if len(rest) > 0 {
		if rest[0] != "tree" || len(rest) < 2 {
			return Repo{}, errors.New("repository URL is not a public github.com URL")
		}
		if ref == "" {
			ref = rest[1]
			rest = rest[2:]
		} else {
			rest = rest[2:]
		}
	}
	if !refOK(ref) {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	sub := strings.Join(rest, "/")
	if !subpathOK(sub) {
		return Repo{}, errors.New("repository URL is not a public github.com URL")
	}
	repo.Ref = ref
	repo.Subpath = sub
	return repo, nil
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

// Fetch downloads the repository archive from base.
// An empty base uses codeload.github.com. The client should refuse redirects
// to other hosts. The body is limited to maxArchive bytes.
func Fetch(ctx context.Context, client *http.Client, base string, repo Repo) ([]byte, error) {
	if !gitName(repo.Owner) || !gitName(repo.Name) || !refOK(repo.Ref) {
		return nil, errors.New("repository URL is not a public github.com URL")
	}
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
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, errPrivate
	}
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("repository archive is unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxArchive+1))
	if err != nil {
		return nil, errors.New("repository archive is unavailable")
	}
	if len(body) > maxArchive {
		return nil, errOversize
	}
	return body, nil
}

// ErrPrivate means the archive was not public.
var errPrivate = errors.New("private")

func IsPrivate(err error) bool { return errors.Is(err, errPrivate) }

func githubRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
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
