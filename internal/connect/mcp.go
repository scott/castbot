package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
)

// RPC is one JSON-RPC object from the guest shim.
type RPC struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// HandleRPC answers initialize, tools/list, and tools/call for the adapter.
// callUpstream performs an already-approved HTTP call. It must not be used
// for a write until the host has an approval.
type Upstream func(ctx context.Context, call Call) (Result, error)

// HandleRPC returns a JSON-RPC response. Write tools return ErrDenied when
// approved is false. The caller waits for approval before calling with approved true.
func HandleRPC(ctx context.Context, connection string, msg RPC, approved bool, upstream Upstream) (json.RawMessage, error) {
	if msg.Method == "" {
		msg.Method = "tools/list"
	}
	switch msg.Method {
	case "initialize":
		return rpcResult(msg.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "castbot-" + connection, "version": "1"},
		})
	case "notifications/initialized", "initialized":
		return nil, nil
	case "ping":
		return rpcResult(msg.ID, map[string]any{})
	case "tools/list":
		return rpcResult(msg.ID, map[string]any{"tools": toolSchemas(connection)})
	case "tools/call":
		return callTool(ctx, connection, msg, approved, upstream)
	default:
		return rpcError(msg.ID, "unknown method"), nil
	}
}

func callTool(ctx context.Context, connection string, msg RPC, approved bool, upstream Upstream) (json.RawMessage, error) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return rpcError(msg.ID, "bad arguments"), nil
		}
	}
	if connection == RequestTool {
		return nil, errRequest
	}
	tool, ok := FindTool(connection, params.Name)
	if !ok {
		return rpcResult(msg.ID, map[string]any{
			"content": []map[string]string{{"type": "text", "text": "unknown tool"}},
			"isError": true,
		})
	}
	if tool.Write && !approved {
		return nil, ErrDenied
	}
	call, err := toolCall(connection, tool, params.Arguments)
	if err != nil {
		return rpcError(msg.ID, "bad arguments"), nil
	}
	if upstream == nil {
		return rpcError(msg.ID, "unavailable"), nil
	}
	res, err := upstream(ctx, call)
	if err != nil {
		return rpcResult(msg.ID, map[string]any{
			"content": []map[string]string{{"type": "text", "text": Code(err)}},
			"isError": true,
		})
	}
	text := string(res.Body)
	if text == "" {
		text = "{}"
	}
	return rpcResult(msg.ID, map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": res.Status >= 400,
	})
}

func toolCall(connection string, tool Tool, args map[string]any) (Call, error) {
	entry, ok := Lookup(connection)
	if !ok || len(entry.Hosts) == 0 {
		return Call{}, ErrAllow
	}
	path, err := fillPath(tool.Path, args)
	if err != nil {
		return Call{}, err
	}
	raw := "https://" + entry.Hosts[0] + path
	if tool.Name == "get_file_contents" || strings.Contains(tool.Path, "/contents/") {
		// contents stay on the API host, which is Hosts[0] for GitHub.
	}
	q := url.Values{}
	for _, key := range tool.Query {
		if val, ok := argString(args, key); ok {
			q.Set(key, val)
		}
	}
	if enc := q.Encode(); enc != "" {
		raw += "?" + enc
	}
	var body []byte
	if tool.Method != "GET" && tool.Method != "HEAD" && tool.Method != "DELETE" {
		payload := map[string]any{}
		for k, v := range args {
			if pathKey(tool.Path, k) || listHas(tool.Query, k) {
				continue
			}
			if secretArg(k) {
				continue
			}
			payload[k] = v
		}
		if len(payload) > 0 {
			body, err = json.Marshal(payload)
			if err != nil {
				return Call{}, ErrBad
			}
		}
	}
	return Call{Connection: connection, Method: tool.Method, URL: raw, Body: body, Tool: tool.Name, Headers: map[string]string{"Accept": "application/json", "Content-Type": "application/json"}}, nil
}

func fillPath(tmpl string, args map[string]any) (string, error) {
	out := tmpl
	for {
		i := strings.Index(out, "{")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], "}")
		if j < 0 {
			return "", ErrBad
		}
		j += i
		key := out[i+1 : j]
		val, ok := argString(args, key)
		if !ok || !pathValue(val) {
			return "", ErrBad
		}
		out = out[:i] + escapePath(val) + out[j+1:]
	}
	if !strings.HasPrefix(out, "/") || strings.Contains(out, "..") {
		return "", ErrBad
	}
	return out, nil
}

func escapePath(v string) string {
	parts := strings.Split(v, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func pathValue(v string) bool {
	if v == "" || len(v) > 240 {
		return false
	}
	if strings.Contains(v, "..") || strings.ContainsAny(v, "\r\n?#\\") {
		return false
	}
	return true
}

func pathKey(tmpl, key string) bool {
	return strings.Contains(tmpl, "{"+key+"}")
}

func listHas(list []string, key string) bool {
	for _, item := range list {
		if item == key {
			return true
		}
	}
	return false
}

func argString(args map[string]any, key string) (string, bool) {
	if args == nil {
		return "", false
	}
	v, ok := args[key]
	if !ok || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t), true
	case float64:
		if t == float64(int64(t)) {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(
				jsonNumber(t), ".0"), "")), true
		}
		return "", false
	default:
		return "", false
	}
}

func jsonNumber(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func secretArg(key string) bool {
	low := strings.ToLower(key)
	switch low {
	case "token", "password", "secret", "authorization", "api_key", "apikey", "access_token":
		return true
	default:
		return strings.Contains(low, "token") || strings.Contains(low, "secret") || strings.Contains(low, "password")
	}
}

func toolSchemas(connection string) []map[string]any {
	if connection == RequestTool {
		return []map[string]any{requestSchema()}
	}
	var out []map[string]any
	for _, tool := range Tools(connection) {
		props := map[string]any{}
		var required []string
		for _, key := range pathKeys(tool.Path) {
			props[key] = map[string]string{"type": "string"}
			required = append(required, key)
		}
		for _, key := range tool.Query {
			props[key] = map[string]string{"type": "string"}
		}
		if tool.Write {
			props["body"] = map[string]string{"type": "object", "description": "Fields sent to the API."}
		}
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			sort.Strings(required)
			schema["required"] = required
		}
		out = append(out, map[string]any{
			"name":        tool.Name,
			"description": tool.Title + ". A write waits for approval. The token stays on the server.",
			"inputSchema": schema,
		})
	}
	return out
}

func requestSchema() map[string]any {
	return map[string]any{
		"name": RequestTool,
		"description": "Ask the user to connect GitHub or DigitalOcean. " +
			"connection is github or digitalocean. reason is a short explanation. " +
			"The value is saved in their vault and is not returned. " +
			"The connection is available on the next turn. " +
			"The result is saved, declined, expired, or already_configured.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"connection": map[string]any{"type": "string", "enum": []string{GitHub, DigitalOcean}},
				"reason":     map[string]string{"type": "string"},
				"name":       map[string]string{"type": "string"},
			},
			"required": []string{"connection"},
		},
	}
}

func pathKeys(tmpl string) []string {
	var out []string
	rest := tmpl
	for {
		i := strings.Index(rest, "{")
		if i < 0 {
			return out
		}
		rest = rest[i+1:]
		j := strings.Index(rest, "}")
		if j < 0 {
			return out
		}
		out = append(out, rest[:j])
		rest = rest[j+1:]
	}
}

func rpcResult(id json.RawMessage, result any) (json.RawMessage, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	env := map[string]any{"jsonrpc": "2.0", "result": json.RawMessage(raw)}
	if len(id) > 0 {
		env["id"] = id
	}
	return json.Marshal(env)
}

func rpcError(id json.RawMessage, message string) json.RawMessage {
	env := map[string]any{
		"jsonrpc": "2.0",
		"error":   map[string]any{"code": -32600, "message": message},
	}
	if len(id) > 0 {
		env["id"] = id
	}
	raw, _ := json.Marshal(env)
	return raw
}

var errRequest = errors.New("request_secret")

// IsRequest reports the request_secret tool, which the host handles itself.
func IsRequest(err error) bool {
	return errors.Is(err, errRequest)
}
