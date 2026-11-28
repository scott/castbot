package connect

func init() {
	register(githubEntry())
}

func githubEntry() Entry {
	return Entry{
		ID: GitHub, Title: "GitHub", SecretName: SecretNameGitHub,
		Detail:     "Fine-grained personal access token. Stored in your vault.",
		TokenLabel: "GitHub token",
		Hosts:      []string{"api.github.com", "github.com"},
		APIHost:    "api.github.com",
		GitHost:    "github.com",
		Auth:       AuthGitHub,
		Aliases:    []string{"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_PERSONAL_ACCESS_TOKEN"},
		Tools:      githubTools,
	}
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
