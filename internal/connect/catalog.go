// Package connect is the host-side catalog for curated connections.
// Tokens stay in the vault. This package does not store them and does not
// put them in a guest environment.
package connect

import "strings"

const (
	GitHub       = "github"
	DigitalOcean = "digitalocean"
	RequestTool  = "request_secret"

	// SecretNameGitHub and SecretNameDigitalOcean are vault names.
	// They are not environment grants and are rejected by the guest pull.
	SecretNameGitHub       = "GITHUB_CONNECTION"
	SecretNameDigitalOcean = "DIGITALOCEAN_CONNECTION"

	RequestTTL = 60 // minutes, documented for callers that do not import time
)

// Entry is one curated connection. Scopes are whatever the owner put on
// the token. This list does not add a service subset.
type Entry struct {
	ID         string
	Title      string
	SecretName string
	Detail     string
	TokenLabel string
	Hosts      []string
}

// Catalog is the only set request_secret will accept.
func Catalog() []Entry {
	return []Entry{
		{
			ID: GitHub, Title: "GitHub", SecretName: SecretNameGitHub,
			Detail:     "Fine-grained personal access token. Stored in your vault.",
			TokenLabel: "GitHub token",
			Hosts:      []string{"api.github.com", "github.com"},
		},
		{
			ID: DigitalOcean, Title: "DigitalOcean", SecretName: SecretNameDigitalOcean,
			Detail:     "API token. You choose what it can access. Stored in your vault.",
			TokenLabel: "DigitalOcean token",
			Hosts:      []string{"api.digitalocean.com"},
		},
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

// ProviderEnv maps a name people type for a provider token onto a catalog id.
// The vault stores the catalog secret name, not this alias.
func ProviderEnv(name string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "GITHUB_TOKEN", "GH_TOKEN", "GITHUB_PERSONAL_ACCESS_TOKEN", SecretNameGitHub:
		return GitHub, true
	case "DIGITALOCEAN_API_TOKEN", "DIGITALOCEAN_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN", SecretNameDigitalOcean:
		return DigitalOcean, true
	default:
		return "", false
	}
}

// BlocksGuest reports a name the guest pull and child environment must reject.
// Generic vault names are not in this set. Connection rows and the provider
// token names people paste as environment variables are, so a mistaken grant
// cannot become an environment variable.
func BlocksGuest(name string) bool {
	_, ok := ProviderEnv(name)
	return ok
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
}

// Tools is the adapter catalog for one connection.
// It is not the full official server. Official processes are not started
// on the small control plane. See the connections doc.
func Tools(connection string) []Tool {
	switch connection {
	case GitHub:
		return githubTools
	case DigitalOcean:
		return digitalOceanTools
	default:
		return nil
	}
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
		for _, tool := range Tools(e.ID) {
			out = append(out, tool.Name)
		}
	}
	return out
}

var githubTools = []Tool{
	{Name: "get_me", Title: "GitHub: current user", Method: "GET", Path: "/user"},
	{Name: "list_repos", Title: "GitHub: list repositories", Method: "GET", Path: "/user/repos", Query: []string{"per_page", "page"}},
	{Name: "get_file_contents", Title: "GitHub: read a file", Method: "GET", Path: "/repos/{owner}/{repo}/contents/{path}"},
	{Name: "list_issues", Title: "GitHub: list issues", Method: "GET", Path: "/repos/{owner}/{repo}/issues", Query: []string{"state", "per_page"}},
	{Name: "get_issue", Title: "GitHub: read an issue", Method: "GET", Path: "/repos/{owner}/{repo}/issues/{issue_number}"},
	{Name: "list_pull_requests", Title: "GitHub: list pull requests", Method: "GET", Path: "/repos/{owner}/{repo}/pulls", Query: []string{"state", "per_page"}},
	{Name: "get_pull_request", Title: "GitHub: read a pull request", Method: "GET", Path: "/repos/{owner}/{repo}/pulls/{pull_number}"},
	{Name: "create_issue", Title: "GitHub: create an issue", Write: true, Method: "POST", Path: "/repos/{owner}/{repo}/issues"},
	{Name: "add_issue_comment", Title: "GitHub: comment on an issue", Write: true, Method: "POST", Path: "/repos/{owner}/{repo}/issues/{issue_number}/comments"},
	{Name: "create_pull_request", Title: "GitHub: create a pull request", Write: true, Method: "POST", Path: "/repos/{owner}/{repo}/pulls"},
	{Name: "create_or_update_file", Title: "GitHub: write a file", Write: true, Method: "PUT", Path: "/repos/{owner}/{repo}/contents/{path}"},
}

var digitalOceanTools = []Tool{
	{Name: "do_account_get", Title: "DigitalOcean: account", Method: "GET", Path: "/v2/account"},
	{Name: "do_droplets_list", Title: "DigitalOcean: list droplets", Method: "GET", Path: "/v2/droplets", Query: []string{"page", "per_page"}},
	{Name: "do_droplet_get", Title: "DigitalOcean: read a droplet", Method: "GET", Path: "/v2/droplets/{droplet_id}"},
	{Name: "do_apps_list", Title: "DigitalOcean: list apps", Method: "GET", Path: "/v2/apps", Query: []string{"page", "per_page"}},
	{Name: "do_droplet_create", Title: "DigitalOcean: create a droplet", Write: true, Method: "POST", Path: "/v2/droplets"},
	{Name: "do_droplet_delete", Title: "DigitalOcean: delete a droplet", Write: true, Method: "DELETE", Path: "/v2/droplets/{droplet_id}"},
}
