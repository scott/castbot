package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

const (
	Todoist           = "todoist"
	SecretNameTodoist = "TODOIST_CONNECTION"
	todoistHost       = "api.todoist.com"
	todoistREST       = "https://api.todoist.com/rest/v2"
)

func init() {
	register(todoistEntry())
}

func todoistEntry() Entry {
	return Entry{
		ID: Todoist, Title: "Todoist", SecretName: SecretNameTodoist,
		Detail:     "API token for this account's tasks. Stored in your vault.",
		TokenLabel: "API token",
		Hosts:      []string{todoistHost},
		APIHost:    todoistHost,
		Auth:       AuthBearer,
		Aliases:    []string{"TODOIST_API_TOKEN", "TODOIST_TOKEN"},
		Tools:      todoistTools,
	}
}

var todoistTools = []Tool{
	{
		Name: "todoist_list_tasks", Title: "Todoist: list tasks", Method: "GET",
		Args:  []Arg{},
		Build: todoistListCall, ReadBody: todoistListBody,
	},
	{
		Name: "todoist_get_task", Title: "Todoist: read a task", Method: "GET",
		Args:  []Arg{{Name: "task", Description: "Task id.", Required: true}},
		Build: todoistGetCall, ReadBody: todoistTaskBody,
	},
	{
		Name: "todoist_create_task", Title: "Todoist: create a task", Write: true, Method: "POST",
		Args:  []Arg{{Name: "content", Description: "Task text.", Required: true}},
		Build: todoistCreateCall, ReadBody: todoistTaskBody,
	},
	{
		Name: "todoist_complete", Title: "Todoist: complete", Write: true, Method: "POST",
		Args:  []Arg{{Name: "task", Description: "Task id.", Required: true}},
		Build: todoistCompleteCall, ReadBody: todoistCompleteBody,
	},
	{
		Name: "todoist_comment", Title: "Todoist: comment", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "task", Description: "Task id.", Required: true},
			{Name: "content", Description: "Comment text.", Required: true},
		},
		Build: todoistCommentCall, ReadBody: todoistCommentBody,
	},
}

func todoistListCall(_ context.Context, args map[string]any) (Call, error) {
	return todoistCall("GET", todoistREST+"/tasks", nil)
}

func todoistGetCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := todoistID(args, "task")
	if err != nil {
		return Call{}, err
	}
	return todoistCall("GET", todoistREST+"/tasks/"+url.PathEscape(id), nil)
}

func todoistCreateCall(_ context.Context, args map[string]any) (Call, error) {
	content, err := todoistText(args, "content", 500)
	if err != nil {
		return Call{}, err
	}
	return todoistCall("POST", todoistREST+"/tasks", map[string]any{"content": content})
}

func todoistCompleteCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := todoistID(args, "task")
	if err != nil {
		return Call{}, err
	}
	return todoistCall("POST", todoistREST+"/tasks/"+url.PathEscape(id)+"/close", nil)
}

func todoistCommentCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := todoistID(args, "task")
	if err != nil {
		return Call{}, err
	}
	content, err := todoistText(args, "content", 2000)
	if err != nil {
		return Call{}, err
	}
	return todoistCall("POST", todoistREST+"/comments", map[string]any{"task_id": id, "content": content})
}

func todoistCall(method, rawURL string, payload any) (Call, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return Call{}, ErrBad
		}
	}
	return Call{
		Method: method,
		URL:    rawURL,
		Headers: map[string]string{
			"Accept":       "application/json",
			"Content-Type": "application/json",
		},
		Body: body,
	}, nil
}

func todoistID(args map[string]any, key string) (string, error) {
	val, ok := argString(args, key)
	if !ok || len(val) < 1 || len(val) > 20 || val[0] == '0' {
		return "", ErrBad
	}
	for _, r := range val {
		if r < '0' || r > '9' {
			return "", ErrBad
		}
	}
	return val, nil
}

func todoistText(args map[string]any, key string, max int) (string, error) {
	val, ok := argString(args, key)
	if !ok || val == "" || len(val) > max || strings.ContainsAny(val, "\r\x00") {
		return "", ErrBad
	}
	return val, nil
}

func todoistClip(body []byte) []byte {
	if len(body) > 256<<10 {
		return body[:256<<10]
	}
	return body
}

func todoistListBody(body []byte) []byte {
	clip := bytes.TrimSpace(todoistClip(body))
	if len(clip) == 0 || clip[0] == '{' {
		if msg, bad := todoistProblem(clip); bad {
			return todoistFail(msg)
		}
		return []byte(`{"error":"bad response"}`)
	}
	var rows []map[string]any
	if json.Unmarshal(clip, &rows) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	out := []map[string]any{}
	for i, row := range rows {
		if i >= 20 {
			break
		}
		out = append(out, todoistTask(row))
	}
	buf, err := json.Marshal(map[string]any{"tasks": out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func todoistTaskBody(body []byte) []byte {
	if msg, bad := todoistProblem(body); bad {
		return todoistFail(msg)
	}
	var row map[string]any
	if json.Unmarshal(todoistClip(body), &row) != nil || todoistScalar(row["id"]) == "" {
		return []byte(`{"error":"bad response"}`)
	}
	buf, err := json.Marshal(todoistTask(row))
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func todoistCompleteBody(body []byte) []byte {
	clip := bytes.TrimSpace(todoistClip(body))
	if len(clip) == 0 {
		return []byte(`{"completed":true}`)
	}
	if msg, bad := todoistProblem(clip); bad {
		return todoistFail(msg)
	}
	return []byte(`{"completed":true}`)
}

func todoistCommentBody(body []byte) []byte {
	if msg, bad := todoistProblem(body); bad {
		return todoistFail(msg)
	}
	var row map[string]any
	if json.Unmarshal(todoistClip(body), &row) != nil || todoistScalar(row["id"]) == "" {
		return []byte(`{"error":"bad response"}`)
	}
	buf, err := json.Marshal(map[string]string{
		"id":      todoistScalar(row["id"]),
		"task_id": todoistScalar(row["task_id"]),
	})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func todoistTask(row map[string]any) map[string]any {
	item := map[string]any{
		"id":        todoistScalar(row["id"]),
		"content":   clipText(todoistScalar(row["content"]), 500),
		"completed": false,
	}
	if done, ok := row["is_completed"].(bool); ok {
		item["completed"] = done
	}
	if due, ok := row["due"].(map[string]any); ok {
		if date := clipText(todoistScalar(due["date"]), 32); date != "" {
			item["due"] = date
		}
	}
	return item
}

func todoistProblem(body []byte) (string, bool) {
	clip := bytes.TrimSpace(todoistClip(body))
	if len(clip) == 0 || clip[0] != '{' {
		return "", false
	}
	var raw map[string]any
	if json.Unmarshal(clip, &raw) != nil {
		return "", false
	}
	if todoistScalar(raw["id"]) != "" {
		return "", false
	}
	msg := todoistScalar(raw["error"])
	if msg == "" {
		return "", false
	}
	return msg, true
}

func todoistFail(message string) []byte {
	buf, err := json.Marshal(map[string]string{"error": clipText(message, 200)})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func todoistScalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return ""
	}
}
