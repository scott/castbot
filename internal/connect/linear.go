package connect

import (
	"context"
	"encoding/json"
	"strings"
)

const (
	Linear           = "linear"
	SecretNameLinear = "LINEAR_CONNECTION"

	linearQueryTeams = `query { teams(first: 50) { nodes { id name key states { nodes { id name } } } } }`

	linearQueryIssues = `query($first: Int!, $filter: IssueFilter) { issues(first: $first, filter: $filter) { nodes { id identifier title url updatedAt state { id name } } } }`

	linearQueryIssue = `query($id: String!) { issue(id: $id) { id identifier title description url state { id name } assignee { name } team { id key states { nodes { id name } } } } }`

	linearQueryComments = `query($id: String!) { issue(id: $id) { comments(first: 50) { nodes { id body createdAt user { name } } } } }`

	linearQueryCreate = `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier title url } } }`

	linearQueryComment = `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success comment { id } } }`

	linearQueryStatus = `mutation($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success issue { id identifier state { id name } } } }`
)

func init() {
	register(linearEntry())
}

func linearEntry() Entry {
	return Entry{
		ID: Linear, Title: "Linear", SecretName: SecretNameLinear,
		Detail:     "Personal API key for issues and comments. Stored in your vault.",
		TokenLabel: "API key",
		Hosts:      []string{"api.linear.app"},
		APIHost:    "api.linear.app",
		Auth:       AuthBearer,
		Aliases:    []string{"LINEAR_API_KEY", "LINEAR_TOKEN"},
		Tools:      linearTools,
	}
}

var linearTools = []Tool{
	{
		Name: "linear_list_teams", Title: "Linear: list teams", Method: "POST",
		Build: linearTeamsCall, ReadBody: linearBody,
	},
	{
		Name: "linear_list_issues", Title: "Linear: list issues", Method: "POST",
		Args: []Arg{
			{Name: "team", Description: "Team id or team key. Optional."},
			{Name: "count", Description: "How many issues, at most 25."},
		},
		Build: linearIssuesCall, ReadBody: linearBody,
	},
	{
		Name: "linear_get_issue", Title: "Linear: read an issue", Method: "POST",
		Args: []Arg{
			{Name: "issue", Description: "Issue id or identifier such as ENG-123.", Required: true},
		},
		Build: linearIssueCall, ReadBody: linearBody,
	},
	{
		Name: "linear_list_comments", Title: "Linear: list comments", Method: "POST",
		Args: []Arg{
			{Name: "issue", Description: "Issue id or identifier.", Required: true},
		},
		Build: linearCommentsCall, ReadBody: linearBody,
	},
	{
		Name: "linear_create_issue", Title: "Linear: create an issue", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "team", Description: "Team id.", Required: true},
			{Name: "title", Description: "Issue title.", Required: true},
			{Name: "description", Description: "Issue text. Optional."},
		},
		Build: linearCreateCall, ReadBody: linearBody,
	},
	{
		Name: "linear_comment", Title: "Linear: comment", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "issue", Description: "Issue id or identifier.", Required: true},
			{Name: "body", Description: "Comment text.", Required: true},
		},
		Build: linearCommentCall, ReadBody: linearBody,
	},
	{
		Name: "linear_update_status", Title: "Linear: update status", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "issue", Description: "Issue id or identifier.", Required: true},
			{Name: "status", Description: "Workflow state id.", Required: true},
		},
		Build: linearStatusCall, ReadBody: linearBody,
	},
}

func linearTeamsCall(context.Context, map[string]any) (Call, error) {
	return linearGraph(linearQueryTeams, map[string]any{})
}

func linearIssuesCall(_ context.Context, args map[string]any) (Call, error) {
	count, err := linearCount(args, 25)
	if err != nil {
		return Call{}, err
	}
	team, err := linearID(args, "team", false)
	if err != nil {
		return Call{}, err
	}
	vars := map[string]any{"first": count, "filter": nil}
	if team != "" {
		field := "key"
		if linearUUID(team) {
			field = "id"
		}
		vars["filter"] = map[string]any{"team": map[string]any{field: map[string]any{"eq": team}}}
	}
	return linearGraph(linearQueryIssues, vars)
}

func linearIssueCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := linearID(args, "issue", true)
	if err != nil {
		return Call{}, err
	}
	return linearGraph(linearQueryIssue, map[string]any{"id": id})
}

func linearCommentsCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := linearID(args, "issue", true)
	if err != nil {
		return Call{}, err
	}
	return linearGraph(linearQueryComments, map[string]any{"id": id})
}

func linearCreateCall(_ context.Context, args map[string]any) (Call, error) {
	team, err := linearID(args, "team", true)
	if err != nil {
		return Call{}, err
	}
	title, err := linearText(args, "title", true, 200)
	if err != nil {
		return Call{}, err
	}
	input := map[string]any{"teamId": team, "title": title}
	if desc, err := linearText(args, "description", false, 8000); err != nil {
		return Call{}, err
	} else if desc != "" {
		input["description"] = desc
	}
	return linearGraph(linearQueryCreate, map[string]any{"input": input})
}

func linearCommentCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := linearID(args, "issue", true)
	if err != nil {
		return Call{}, err
	}
	body, err := linearText(args, "body", true, 8000)
	if err != nil {
		return Call{}, err
	}
	return linearGraph(linearQueryComment, map[string]any{"input": map[string]any{"issueId": id, "body": body}})
}

func linearStatusCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := linearID(args, "issue", true)
	if err != nil {
		return Call{}, err
	}
	state, err := linearID(args, "status", true)
	if err != nil {
		return Call{}, err
	}
	return linearGraph(linearQueryStatus, map[string]any{
		"id":    id,
		"input": map[string]any{"stateId": state},
	})
}

func linearGraph(query string, vars map[string]any) (Call, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return Call{}, ErrBad
	}
	return Call{
		Method:  "POST",
		URL:     "https://api.linear.app/graphql",
		Headers: map[string]string{"Accept": "application/json", "Content-Type": "application/json"},
		Body:    body,
	}, nil
}

func linearBody(body []byte) []byte {
	if len(body) > 256<<10 {
		body = body[:256<<10]
	}
	if !json.Valid(body) {
		return []byte(`{"error":"bad response"}`)
	}
	return body
}

func linearCount(args map[string]any, max int) (int, error) {
	raw, ok := argString(args, "count")
	if !ok || raw == "" {
		return max, nil
	}
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' || n > 1000 {
			return 0, ErrBad
		}
		n = n*10 + int(r-'0')
	}
	if n < 1 {
		return 0, ErrBad
	}
	if n > max {
		n = max
	}
	return n, nil
}

func linearID(args map[string]any, key string, required bool) (string, error) {
	val, ok := argString(args, key)
	if !ok || val == "" {
		if required {
			return "", ErrBad
		}
		return "", nil
	}
	if len(val) > 80 || strings.ContainsAny(val, "\r\n\x00{}") {
		return "", ErrBad
	}
	return val, nil
}

func linearText(args map[string]any, key string, required bool, max int) (string, error) {
	val, ok := argString(args, key)
	if !ok || strings.TrimSpace(val) == "" {
		if required {
			return "", ErrBad
		}
		return "", nil
	}
	if strings.ContainsAny(val, "\r\x00") || len(val) > max {
		return "", ErrBad
	}
	return val, nil
}

func linearUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return false
			}
		}
	}
	return true
}
