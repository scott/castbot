// Package connect is the host-side catalog for curated connections.
// Tokens stay in the vault. This package does not store them and does not
// put them in a guest environment.
package connect

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"github.com/scott/castbot/internal/egresscontract"
)

const (
	GitHub       = "github"
	DigitalOcean = "digitalocean"
	RequestTool  = "request_secret"

	// SecretNameGitHub and SecretNameDigitalOcean are vault names.
	// They are not environment grants and are rejected by the guest pull.
	SecretNameGitHub       = "GITHUB_CONNECTION"
	SecretNameDigitalOcean = "DIGITALOCEAN_CONNECTION"

	RequestTTL = 60 // minutes, documented for callers that do not import time

	// AuthBearer sends Authorization: Bearer. It is the default.
	AuthBearer = "bearer"
	// AuthGitHub sends bearer on the API host, basic x-access-token on the
	// git host, and the GitHub API version header.
	AuthGitHub = "github"
	// AuthBasic sends the token as the HTTP basic user, with an empty password.
	AuthBasic = "basic"
	// AuthBasicPair sends the extra field as the user and the token as the password.
	AuthBasicPair = "basic-pair"
	// AuthHeader sets HeaderName to the token and does not set Authorization.
	AuthHeader = "header"
)

// Entry is one curated connection. Scopes are whatever the owner put on
// the token. This list does not add a service subset.
// A new connection is a file that calls register from init. It does not
// edit a switch in this file.
type Entry struct {
	ID         string
	Title      string
	SecretName string
	Detail     string
	TokenLabel string
	// Hosts is the allowlist. APIHost and GitHost must be in this list.
	Hosts []string
	// APIHost is the host for /egress/http. The guest does not choose it.
	APIHost string
	// GitHost is the host for /egress/git. Empty means git is refused.
	GitHost string
	// Auth is bearer, github, basic, basic-pair, or header.
	Auth string
	// HeaderName is the header AuthHeader sets to the vault token.
	HeaderName string
	// RemoteMCP is a hosted MCP URL. The in-process adapter is not used.
	RemoteMCP string
	// Aliases are environment names people paste for this token.
	// The vault stores SecretName, not an alias.
	Aliases []string
	// ExtraLabel is a second settings field stored in the same vault row.
	// The agent cannot choose that value. Empty means the token is the
	// whole vault value.
	ExtraLabel string
	Tools      []Tool
	// Retired are tool names the host must not forward.
	Retired []string
}

// Tool is one host-adapter tool. Write tools need an approval on the host.
type Tool struct {
	Name   string
	Title  string
	Write  bool
	Method string
	// Path is an upstream path template. {name} is filled from arguments.
	Path string
	// Query lists argument names copied to the query string.
	Query []string
	// Args, when set, are the tool's input fields. The free write body is
	// not offered. Path and query keys are used when Args is empty.
	Args []Arg
	// Build, when set, replaces the path template. The URL it returns
	// must stay on this connection's allowlist unless PublicURL is set.
	// The context carries the vault secret and the optional extra field.
	Build func(ctx context.Context, args map[string]any) (Call, error)
	// PublicURL lets Build return one public https URL. The host is not
	// on the connection allowlist. The dial still refuses a private address.
	PublicURL bool
	// ReadBody, when set, replaces the upstream body before the guest sees it.
	ReadBody func(body []byte) []byte
	// Run, when set, performs the tool. Mail protocols that are not one
	// HTTPS request use it. The context carries the vault secret and the
	// extra field. A write still waits for approval before Run is called.
	Run func(ctx context.Context, args map[string]any) ([]byte, error)
}

// Arg is one tool argument the model may pass.
type Arg struct {
	Name        string
	Description string
	Required    bool
}

var registry []Entry

// register adds one connection. A duplicate or inconsistent entry panics
// so a bad file fails at startup. init is the only caller.
func register(e Entry) {
	if e.ID != strings.ToLower(strings.TrimSpace(e.ID)) || !egresscontract.KnownProvider(e.ID) {
		panic("connection id " + e.ID)
	}
	for _, cur := range registry {
		if cur.ID == e.ID {
			panic("duplicate connection " + e.ID)
		}
	}
	if len(e.Hosts) == 0 || e.SecretName == "" || e.SecretName != strings.ToUpper(e.SecretName) {
		panic("connection " + e.ID + " is incomplete")
	}
	if e.APIHost != "" && !hostAllowed(e, e.APIHost) {
		panic("connection " + e.ID + " api host")
	}
	if e.GitHost != "" && !hostAllowed(e, e.GitHost) {
		panic("connection " + e.ID + " git host")
	}
	switch e.Auth {
	case "", AuthBearer, AuthGitHub, AuthBasic, AuthBasicPair:
	case AuthHeader:
		if strings.TrimSpace(e.HeaderName) == "" {
			panic("connection " + e.ID + " header")
		}
	default:
		panic("connection " + e.ID + " auth")
	}
	if e.RemoteMCP != "" {
		u, err := url.Parse(e.RemoteMCP)
		if err != nil || u.Scheme != "https" || u.User != nil || !hostAllowed(e, u.Hostname()) {
			panic("connection " + e.ID + " remote")
		}
	}
	registry = append(registry, e)
}

// Catalog is the only set request_secret will accept.
// GitHub and DigitalOcean stay first. Later entries are sorted by id.
func Catalog() []Entry {
	out := append([]Entry(nil), registry...)
	sort.Slice(out, func(i, j int) bool {
		ri, rj := catalogRank(out[i].ID), catalogRank(out[j].ID)
		if ri != rj {
			return ri < rj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func catalogRank(id string) int {
	switch id {
	case GitHub:
		return 0
	case DigitalOcean:
		return 1
	default:
		return 2
	}
}

// Lookup returns a catalog entry. Unknown ids are rejected.
func Lookup(id string) (Entry, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, e := range Catalog() {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// SecretName is the vault name for a connection, or empty.
func SecretName(id string) string {
	e, ok := Lookup(id)
	if !ok {
		return ""
	}
	return e.SecretName
}

// VaultName reports a connection vault name. Aliases are not vault names.
// An alias may already exist as an environment row so it can be moved.
func VaultName(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, e := range Catalog() {
		if strings.EqualFold(e.SecretName, name) {
			return true
		}
	}
	return false
}

// ProviderEnv maps a name people type for a provider token onto a catalog id.
// The vault stores the catalog secret name, not this alias.
func ProviderEnv(name string) (string, bool) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return "", false
	}
	for _, e := range Catalog() {
		if strings.EqualFold(e.SecretName, name) {
			return e.ID, true
		}
		for _, alias := range e.Aliases {
			if strings.EqualFold(alias, name) {
				return e.ID, true
			}
		}
	}
	return "", false
}

// BlocksGuest reports a name the guest pull and child environment must reject.
// Generic vault names are not in this set. Connection rows and the provider
// token names people paste as environment variables are, so a mistaken grant
// cannot become an environment variable.
func BlocksGuest(name string) bool {
	_, ok := ProviderEnv(name)
	return ok
}

// Tools is the policy catalog for one connection.
// GitHub entries are the in-process adapter. A hosted MCP connection lists
// its router tools here. They are not turned into API requests.
// See the connections doc.
func Tools(connection string) []Tool {
	e, ok := Lookup(connection)
	if !ok {
		return nil
	}
	return e.Tools
}

// RetiredTool reports a tool name that must not be forwarded.
func RetiredTool(connection, name string) bool {
	e, ok := Lookup(connection)
	if !ok {
		return false
	}
	for _, tool := range e.Retired {
		if tool == name {
			return true
		}
	}
	return false
}

// FindTool returns one adapter tool.
func FindTool(connection, name string) (Tool, bool) {
	for _, tool := range Tools(connection) {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

// PermNames are permission keys this package writes when a connection changes.
func PermNames() []string {
	out := []string{"mcp", RequestTool}
	for _, e := range Catalog() {
		for _, tool := range e.Tools {
			out = append(out, tool.Name)
		}
		out = append(out, e.Retired...)
	}
	return out
}
