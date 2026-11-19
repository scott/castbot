package guestwrap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/scott/castbot/internal/agentskill"
	"github.com/scott/castbot/internal/egresscontract"
	"github.com/scott/castbot/internal/routinetool"
)

// ServeMCP speaks newline-delimited JSON-RPC on stdin and stdout.
// It returns when stdin ends. A missing egress becomes a JSON-RPC error
// and does not block.
func ServeMCP(server, base string, in io.Reader, out io.Writer) error {
	if !allowedMCP(server) {
		return errMCP
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 2<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			if err := writeJSON(out, rpcError(nil, egresscontract.Rejected)); err != nil {
				return err
			}
			continue
		}
		if msg.Method == "" || msg.ID == nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			proto := "2024-11-05"
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			if strings.HasPrefix(params.ProtocolVersion, "202") {
				proto = params.ProtocolVersion
			}
			if err := writeJSON(out, rpcResult(msg.ID, map[string]any{
				"protocolVersion": proto,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "castbot-" + server, "version": "1"},
			})); err != nil {
				return err
			}
		case "ping":
			if err := writeJSON(out, rpcResult(msg.ID, map[string]any{})); err != nil {
				return err
			}
		case "tools/list":
			if server == "request_secret" {
				if err := writeJSON(out, rpcResult(msg.ID, map[string]any{"tools": []any{requestSecretTool()}})); err != nil {
					return err
				}
				continue
			}
			if server == agentskill.ServerName {
				if err := writeJSON(out, rpcResult(msg.ID, map[string]any{"tools": agentTools()})); err != nil {
					return err
				}
				continue
			}
			reply, err := postJSON(base+"/mcp", egresscontract.MCPCall{Server: server, Message: json.RawMessage(line)}, 60*time.Second)
			if err != nil {
				if err := writeJSON(out, rpcError(msg.ID, egresscontract.NotConnected)); err != nil {
					return err
				}
				continue
			}
			if err := writeRaw(out, reply); err != nil {
				return err
			}
		case "tools/call":
			if server == "request_secret" {
				text, isErr := callSecret(base, msg.Params)
				if err := writeJSON(out, rpcResult(msg.ID, toolResult(text, isErr))); err != nil {
					return err
				}
				continue
			}
			if server == agentskill.ServerName {
				text, isErr := callAgents(base, msg.Params)
				if err := writeJSON(out, rpcResult(msg.ID, toolResult(text, isErr))); err != nil {
					return err
				}
				continue
			}
			reply, err := postJSON(base+"/mcp", egresscontract.MCPCall{Server: server, Message: json.RawMessage(line)}, 60*time.Second)
			if err != nil {
				if err := writeJSON(out, rpcError(msg.ID, egresscontract.NotConnected)); err != nil {
					return err
				}
				continue
			}
			if err := writeRaw(out, reply); err != nil {
				return err
			}
		default:
			if err := writeJSON(out, rpcError(msg.ID, egresscontract.Rejected)); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return nil
}

func allowedMCP(server string) bool {
	switch server {
	case "github", "digitalocean", "request_secret", agentskill.ServerName:
		return true
	default:
		return false
	}
}

func agentTools() []any {
	return []any{
		map[string]any{
			"name":        agentskill.ToolCreate,
			"description": "Create a new agent in this account from a description the person in this chat asked for.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description": map[string]any{"type": "string", "description": "What the new agent should do, in the person's words."},
					"name":        map[string]any{"type": "string", "description": "Optional name the person asked for."},
				},
				"required": []string{"description"},
			},
		},
		map[string]any{
			"name":        agentskill.ToolList,
			"description": "List the agents in this account.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		map[string]any{
			"name":        routinetool.ToolList,
			"description": "List this agent's routines.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		map[string]any{
			"name":        routinetool.ToolCreate,
			"description": "Create a routine for this agent when the person asked for one.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":     map[string]any{"type": "string", "description": "Name the person asked for."},
					"cron":     map[string]any{"type": "string", "description": "5 fields: minute hour day month weekday."},
					"prompt":   map[string]any{"type": "string", "description": "What the agent should do."},
					"timezone": map[string]any{"type": "string", "description": "IANA time zone, only when the person named one."},
					"missed":   map[string]any{"type": "string", "enum": []string{"once", "skip"}, "description": "What happens when a run is missed, only when the person named it."},
					"approval": map[string]any{"type": "string", "enum": []string{"wait", "deny"}, "description": "What happens when a step needs approval, only when the person named it."},
				},
				"required": []string{"name", "cron", "prompt"},
			},
		},
		map[string]any{
			"name":        routinetool.ToolUpdate,
			"description": "Change one of this agent's routines. Set enabled to false to pause and true to resume.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":       map[string]any{"type": "string", "description": "Routine id from list_routines."},
					"name":     map[string]any{"type": "string"},
					"cron":     map[string]any{"type": "string", "description": "5 fields: minute hour day month weekday."},
					"prompt":   map[string]any{"type": "string"},
					"timezone": map[string]any{"type": "string", "description": "IANA time zone."},
					"missed":   map[string]any{"type": "string", "enum": []string{"once", "skip"}},
					"approval": map[string]any{"type": "string", "enum": []string{"wait", "deny"}},
					"enabled":  map[string]any{"type": "boolean", "description": "False pauses the routine. True resumes it."},
				},
				"required": []string{"id"},
			},
		},
		map[string]any{
			"name":        routinetool.ToolDelete,
			"description": "Delete one of this agent's routines.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "Routine id from list_routines."},
				},
				"required": []string{"id"},
			},
		},
	}
}

func callAgents(base string, params json.RawMessage) (string, bool) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return egresscontract.Rejected, true
	}
	var args struct {
		Description string `json:"description"`
		Name        string `json:"name"`
	}
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	var payload any
	switch call.Name {
	case agentskill.ToolCreate:
		payload = map[string]any{
			"name": call.Name,
			"arguments": map[string]string{
				"description": args.Description,
				"name":        args.Name,
			},
		}
	case agentskill.ToolList, routinetool.ToolList:
		payload = map[string]any{"name": call.Name, "arguments": map[string]any{}}
	case routinetool.ToolCreate, routinetool.ToolUpdate, routinetool.ToolDelete:
		forwarded, ok := routineForwardArgs(call.Arguments)
		if !ok {
			return egresscontract.Rejected, true
		}
		payload = map[string]any{"name": call.Name, "arguments": forwarded}
	default:
		return egresscontract.Rejected, true
	}
	body, err := postJSON(base+"/agents", payload, 60*time.Second)
	if err != nil {
		return egresscontract.NotConnected, true
	}
	var result struct {
		Text  string `json:"text"`
		Error bool   `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil || strings.TrimSpace(result.Text) == "" {
		return egresscontract.Rejected, true
	}
	return result.Text, result.Error
}

func routineForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	delete(args, "user_id")
	delete(args, "agent_id")
	return args, true
}

func requestSecretTool() map[string]any {
	return map[string]any{
		"name":        "request_secret",
		"description": "Ask to connect GitHub or DigitalOcean. The value is saved on the host and is not returned to the agent. A successful connect is available on the next turn. The result is saved, declined, expired, or already_configured.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"connection": map[string]any{"type": "string", "enum": []string{"github", "digitalocean"}},
				"reason":     map[string]any{"type": "string"},
				"name":       map[string]any{"type": "string"},
			},
			"required": []string{"connection"},
		},
	}
}

func callSecret(base string, params json.RawMessage) (string, bool) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(params, &call)
	if call.Name != "request_secret" {
		return egresscontract.Rejected, true
	}
	var args struct {
		Connection string `json:"connection"`
		Reason     string `json:"reason"`
		Name       string `json:"name"`
	}
	_ = json.Unmarshal(call.Arguments, &args)
	if !egresscontract.KnownProvider(args.Connection) {
		return egresscontract.Rejected, true
	}
	body, err := postJSON(base+"/secret-request", egresscontract.SecretCall{
		Connection: args.Connection,
		Reason:     args.Reason,
		Name:       args.Name,
	}, 61*time.Minute)
	if err != nil {
		return egresscontract.NotConnected, true
	}
	var result egresscontract.SecretResult
	if err := json.Unmarshal(body, &result); err != nil {
		return egresscontract.Rejected, true
	}
	if egresscontract.KnownSecretResult(result.Result) {
		return result.Result, false
	}
	return egresscontract.SafeMessage(result.Error), true
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isErr,
	}
}

type rpcMessage struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
}

func rpcResult(id *json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func rpcError(id *json.RawMessage, message string) map[string]any {
	if id == nil {
		empty := json.RawMessage("null")
		id = &empty
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32000, "message": message},
	}
}

func writeJSON(out io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = out.Write(append(raw, '\n'))
	return err
}

// postJSON posts to the local egress and returns the JSON-RPC message
// object, or the raw secret-result JSON. A non-200 becomes an error so
// the caller can emit the fixed sentence. The response body is not
// included in the error.
func postJSON(url string, payload any, timeout time.Duration) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := localClient(timeout).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, egresscontract.BodyCap+1))
	if err != nil || len(body) > egresscontract.BodyCap || res.StatusCode != http.StatusOK {
		return nil, errMCP
	}
	if strings.HasSuffix(url, "/mcp") {
		var wrapped struct {
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(body, &wrapped); err != nil || len(wrapped.Message) == 0 {
			return nil, errMCP
		}
		if wrapped.Message[0] != '{' {
			return nil, errMCP
		}
		return append(wrapped.Message, '\n'), nil
	}
	return body, nil
}

func writeRaw(out io.Writer, msg json.RawMessage) error {
	if len(msg) == 0 || msg[len(msg)-1] != '\n' {
		msg = append(msg, '\n')
	}
	_, err := out.Write(msg)
	return err
}

var errMCP = errors.New("mcp")
