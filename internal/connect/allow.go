package connect

import (
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// CheckURL accepts an https URL whose host is on the connection allowlist.
// IP literals, userinfo, and other ports are refused. The caller still has
// to resolve the name and refuse a private address.
func CheckURL(connection, raw string) (*url.URL, error) {
	entry, ok := Lookup(connection)
	if !ok {
		return nil, errAllow
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return nil, errAllow
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, errAllow
	}
	if u.User != nil {
		return nil, errAllow
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || strings.Contains(host, "..") {
		return nil, errAllow
	}
	if net.ParseIP(host) != nil {
		return nil, errAllow
	}
	port := u.Port()
	if port != "" && port != "443" {
		return nil, errAllow
	}
	if !hostAllowed(entry, host) {
		return nil, errAllow
	}
	u.Host = host
	if port == "443" {
		u.Host = host + ":443"
	}
	return u, nil
}

func hostAllowed(entry Entry, host string) bool {
	for _, name := range entry.Hosts {
		if host == name {
			return true
		}
	}
	return false
}

// HostAllowed reports whether host is on the connection allowlist.
func HostAllowed(connection, host string) bool {
	entry, ok := Lookup(connection)
	if !ok {
		return false
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return hostAllowed(entry, host)
}

// PublicIP reports whether ip is a global unicast address.
// Loopback, private, link-local, multicast, and carrier-grade NAT are refused.
func PublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 is shared address space, not a public destination.
		if v4[0] == 100 && v4[1]&0xc0 == 64 {
			return false
		}
		if v4[0] == 0 {
			return false
		}
	}
	return true
}

var (
	gitUploadPath  = regexp.MustCompile(`^/[^/]+/[^/]+(?:\.git)?/git-upload-pack$`)
	gitReceivePath = regexp.MustCompile(`^/[^/]+/[^/]+(?:\.git)?/git-receive-pack$`)
	gitInfoPath    = regexp.MustCompile(`^/[^/]+/[^/]+(?:\.git)?/info/refs$`)
)

// Mutating reports whether a proxied call can change upstream state.
// A git fetch is a read only when it is real git smart HTTP on github.com:
// GET .../info/refs?service=git-upload-pack, or POST .../git-upload-pack.
// The words are not a substring match, so an API URL cannot opt out of approval
// by mentioning git-upload-pack. git receive-pack stays a write, including
// GET discovery on info/refs.
func Mutating(method, rawURL string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	if gitSmart(method, rawURL) == gitWrite {
		return true
	}
	if gitSmart(method, rawURL) == gitRead {
		return false
	}
	switch method {
	case "GET", "HEAD":
		return false
	default:
		return true
	}
}

const (
	gitNone = iota
	gitRead
	gitWrite
)

func gitSmart(method, rawURL string) int {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil {
		return gitNone
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host != "github.com" && host != "www.github.com" {
		return gitNone
	}
	cleaned := gitSmartPath(u.Path)
	if cleaned == "" {
		return gitNone
	}
	if gitReceivePath.MatchString(cleaned) {
		return gitWrite
	}
	if gitInfoPath.MatchString(cleaned) && queryIs(u.RawQuery, "service", "git-receive-pack") {
		return gitWrite
	}
	if method == "POST" && gitUploadPath.MatchString(cleaned) {
		return gitRead
	}
	if method == "GET" && gitInfoPath.MatchString(cleaned) && queryIs(u.RawQuery, "service", "git-upload-pack") && u.RawQuery == "service=git-upload-pack" {
		return gitRead
	}
	return gitNone
}

func gitSmartPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.ContainsAny(p, "\\\x00") {
		return ""
	}
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/") || strings.Contains(cleaned, "..") {
		return ""
	}
	return cleaned
}

func queryIs(raw, key, want string) bool {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return false
	}
	vals, ok := q[key]
	return ok && len(vals) == 1 && vals[0] == want
}

// MethodOK is the verb set the proxy will forward.
func MethodOK(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

// APIURL is the allowlisted API URL for a guest path. The guest does not
// choose the host. github maps to api.github.com. digitalocean maps to
// api.digitalocean.com.
func APIURL(provider, pth, query string) (string, error) {
	return upstreamURL(provider, false, pth, query)
}

// GitURL is git smart HTTP. Only GitHub has a git host.
func GitURL(provider, pth, query string) (string, error) {
	return upstreamURL(provider, true, pth, query)
}

func upstreamURL(provider string, git bool, pth, query string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	var host string
	switch provider {
	case GitHub:
		host = "api.github.com"
		if git {
			host = "github.com"
		}
	case DigitalOcean:
		if git {
			return "", errBad
		}
		host = "api.digitalocean.com"
	default:
		return "", errBad
	}
	cleaned, ok := cleanForwardPath(pth)
	if !ok {
		return "", errBad
	}
	query = strings.TrimPrefix(strings.TrimSpace(query), "?")
	if len(query) > 4096 || strings.ContainsAny(query, "\r\n") || strings.Contains(query, "://") {
		return "", errBad
	}
	raw := "https://" + host + cleaned
	if query != "" {
		raw += "?" + query
	}
	if _, err := CheckURL(provider, raw); err != nil {
		return "", errBad
	}
	return raw, nil
}

func cleanForwardPath(p string) (string, bool) {
	if p == "" || !strings.HasPrefix(p, "/") || len(p) > 2048 {
		return "", false
	}
	lower := strings.ToLower(p)
	if strings.Contains(p, "..") || strings.Contains(p, "://") || strings.ContainsAny(p, "\\\x00\r\n") ||
		strings.Contains(lower, "%2e") || strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%00") {
		return "", false
	}
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/") || strings.Contains(cleaned, "..") {
		return "", false
	}
	return cleaned, true
}
