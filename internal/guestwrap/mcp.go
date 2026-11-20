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
	"github.com/scott/castbot/internal/approvetool"
	"github.com/scott/castbot/internal/chattool"
	"github.com/scott/castbot/internal/connectiontool"
	"github.com/scott/castbot/internal/deletetool"
	"github.com/scott/castbot/internal/edittool"
	"github.com/scott/castbot/internal/egresscontract"
	"github.com/scott/castbot/internal/grouptool"
	"github.com/scott/castbot/internal/importtool"
	"github.com/scott/castbot/internal/machinetool"
	"github.com/scott/castbot/internal/replacetool"
	"github.com/scott/castbot/internal/routinetool"
	"github.com/scott/castbot/internal/secretask"
	"github.com/scott/castbot/internal/secretstool"
	"github.com/scott/castbot/internal/selftool"
	"github.com/scott/castbot/internal/settingstool"
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
				if err := writeJSON(out, rpcResult(msg.ID, map[string]any{"tools": []any{requestSecretTool(), deleteSecretTool()}})); err != nil {
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
	tools := []any{
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
		map[string]any{
			"name":        routinetool.ToolRun,
			"description": "Start one of this agent's routines now, in its own thread. Only when the person asked.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "Routine id from list_routines."},
				},
				"required": []string{"id"},
			},
		},
	}
	for _, spec := range grouptool.Specs() {
		tools = append(tools, organizeSchema(spec))
	}
	for _, spec := range selftool.Specs() {
		tools = append(tools, selfSchema(spec))
	}
	for _, spec := range machinetool.Specs() {
		tools = append(tools, machineSchema(spec))
	}
	for _, spec := range edittool.Specs() {
		tools = append(tools, editSchema(spec))
	}
	for _, spec := range approvetool.Specs() {
		tools = append(tools, approvalSchema(spec))
	}
	for _, spec := range chattool.Specs() {
		tools = append(tools, chatSchema(spec))
	}
	for _, spec := range settingstool.Specs() {
		tools = append(tools, settingsSchema(spec))
	}
	for _, spec := range connectiontool.Specs() {
		tools = append(tools, connectionSchema(spec))
	}
	for _, spec := range secretstool.Specs() {
		tools = append(tools, vaultSchema(spec))
	}
	for _, spec := range replacetool.Specs() {
		tools = append(tools, replaceSchema(spec))
	}
	for _, spec := range deletetool.Specs() {
		tools = append(tools, deleteSchema(spec))
	}
	for _, spec := range importtool.Specs() {
		tools = append(tools, importSchema(spec))
	}
	return tools
}

func organizeSchema(spec grouptool.Spec) map[string]any {
	props := map[string]any{}
	var required []string
	group := map[string]any{"type": "string", "description": "Group title or id."}
	people := map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "People on this account, by name or email."}
	switch spec.Name {
	case grouptool.ToolCreateGroup:
		props["title"] = map[string]any{"type": "string", "description": "Title the person asked for."}
		props["agents"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "At least two of this person's agents, by name or id."}
		props["people"] = people
		required = []string{"title", "agents"}
	case grouptool.ToolUpdateGroup:
		props["group"] = group
		props["title"] = map[string]any{"type": "string", "description": "New title."}
		required = []string{"group", "title"}
	case grouptool.ToolAddMembers, grouptool.ToolRemoveMembers:
		props["group"] = group
		props["agents"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Agent names or ids."}
		props["people"] = people
		required = []string{"group"}
	case grouptool.ToolDeleteGroup, grouptool.ToolPinGroup, grouptool.ToolUnpinGroup:
		props["group"] = group
		required = []string{"group"}
	case grouptool.ToolCreateSection:
		props["name"] = map[string]any{"type": "string", "description": "Section name."}
		required = []string{"name"}
	case grouptool.ToolRenameSection:
		props["section"] = map[string]any{"type": "string", "description": "Section name or id."}
		props["name"] = map[string]any{"type": "string", "description": "New name."}
		required = []string{"section", "name"}
	case grouptool.ToolDeleteSection:
		props["section"] = map[string]any{"type": "string", "description": "Section name or id."}
		required = []string{"section"}
	case grouptool.ToolMoveAgents:
		props["agents"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Agents to move, by name or id."}
		props["section"] = map[string]any{"type": "string", "description": "Section name or id. Omit when out is true."}
		props["out"] = map[string]any{"type": "boolean", "description": "True puts the agents back in Agents."}
		required = []string{"agents"}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": schema,
	}
}

func machineSchema(spec machinetool.Spec) map[string]any {
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func selfSchema(spec selftool.Spec) map[string]any {
	props := map[string]any{}
	var required []string
	if spec.Query {
		queryDesc := "Words to match. Omit unless the person asked to look for a word."
		if spec.QueryRequired {
			queryDesc = "Words to match."
			required = []string{"query"}
		}
		props["query"] = map[string]any{"type": "string", "description": queryDesc}
	}
	if spec.ID {
		props["id"] = map[string]any{"type": "string", "description": "Routine id from list_routines. Omit to list recent runs."}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": schema,
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
	case routinetool.ToolCreate, routinetool.ToolUpdate, routinetool.ToolDelete, routinetool.ToolRun:
		forwarded, ok := routineForwardArgs(call.Arguments)
		if !ok {
			return egresscontract.Rejected, true
		}
		payload = map[string]any{"name": call.Name, "arguments": forwarded}
	default:
		if selftool.Known(call.Name) {
			forwarded, ok := selfForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if machinetool.Known(call.Name) {
			forwarded, ok := machineForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if edittool.Known(call.Name) {
			forwarded, ok := editForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if approvetool.Known(call.Name) {
			forwarded, ok := approvalForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if chattool.Known(call.Name) {
			forwarded, ok := chatForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if settingstool.Known(call.Name) {
			forwarded, ok := settingsForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if connectiontool.Known(call.Name) {
			forwarded, ok := connectionForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if secretstool.Known(call.Name) {
			forwarded, ok := vaultForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if replacetool.Known(call.Name) {
			forwarded, ok := replaceForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if deletetool.Known(call.Name) {
			forwarded, ok := deleteForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if importtool.Known(call.Name) {
			forwarded, ok := importForwardArgs(call.Arguments)
			if !ok {
				return egresscontract.Rejected, true
			}
			payload = map[string]any{"name": call.Name, "arguments": forwarded}
			break
		}
		if !grouptool.Known(call.Name) {
			return egresscontract.Rejected, true
		}
		forwarded, ok := groupForwardArgs(call.Arguments)
		if !ok {
			return egresscontract.Rejected, true
		}
		payload = map[string]any{"name": call.Name, "arguments": forwarded}
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

func selfForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	if q, ok := args["query"].(string); ok {
		out["query"] = q
	}
	if id, ok := args["id"].(string); ok {
		out["id"] = id
	}
	return out, true
}

func editSchema(spec edittool.Spec) map[string]any {
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":         map[string]any{"type": "string", "description": "Sidebar name, up to 40 characters."},
				"title":        map[string]any{"type": "string", "description": "Title, up to 80 characters. An empty string clears it."},
				"job":          map[string]any{"type": "string", "description": "What other agents see, up to 160 characters. An empty string clears it."},
				"instructions": map[string]any{"type": "string", "description": "Replacement for instructions.md."},
				"color":        map[string]any{"type": "string", "description": "Coral, Mint, Violet, Gold, Sky, Rose, Lime, Sand, or Aqua."},
				"model":        map[string]any{"type": "string", "description": "Model id from the provider list."},
				"delegate":     map[string]any{"type": "boolean", "description": "True lets this agent message the other agents on this account."},
			},
		},
	}
}

func machineForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	return map[string]any{}, true
}

func chatSchema(spec chattool.Spec) map[string]any {
	props := map[string]any{}
	if spec.Thread {
		props["thread"] = map[string]any{"type": "string", "description": "Thread title or id. Leave it out when only one thread matches."}
	}
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": props,
		},
	}
}

func settingsSchema(spec settingstool.Spec) map[string]any {
	props := map[string]any{}
	var required []string
	for _, arg := range spec.Args {
		props[arg.Name] = map[string]any{"type": "string", "description": arg.Description}
		if arg.Required {
			required = append(required, arg.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": schema,
	}
}

func connectionSchema(spec connectiontool.Spec) map[string]any {
	props := map[string]any{}
	var required []string
	for _, arg := range spec.Args {
		props[arg.Name] = map[string]any{"type": "string", "description": arg.Description}
		if arg.Required {
			required = append(required, arg.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": schema,
	}
}

func vaultSchema(spec secretstool.Spec) map[string]any {
	props := map[string]any{}
	var required []string
	for _, arg := range spec.Args {
		props[arg.Name] = map[string]any{"type": "string", "description": arg.Description}
		if arg.Required {
			required = append(required, arg.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": schema,
	}
}

func replaceSchema(spec replacetool.Spec) map[string]any {
	props := map[string]any{}
	var required []string
	for _, arg := range spec.Args {
		props[arg.Name] = map[string]any{"type": "string", "description": arg.Description}
		if arg.Required {
			required = append(required, arg.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": schema,
	}
}

func deleteSchema(spec deletetool.Spec) map[string]any {
	props := map[string]any{}
	var required []string
	for _, arg := range spec.Args {
		props[arg.Name] = map[string]any{"type": "string", "description": arg.Description}
		if arg.Required {
			required = append(required, arg.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": schema,
	}
}

func importSchema(spec importtool.Spec) map[string]any {
	props := map[string]any{}
	var required []string
	for _, arg := range spec.Args {
		props[arg.Name] = map[string]any{"type": "string", "description": arg.Description}
		if arg.Required {
			required = append(required, arg.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": schema,
	}
}

func importForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	for _, key := range []string{"url", "archive_b64"} {
		value, ok := args[key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out[key] = text
	}
	return out, true
}

func deleteForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	if value, ok := args["confirm"]; ok {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out["confirm"] = text
	}
	return out, true
}

func replaceForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	if value, ok := args["confirm"]; ok {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out["confirm"] = text
	}
	return out, true
}

func vaultForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	for _, key := range []string{"secret", "agent"} {
		value, ok := args[key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out[key] = text
	}
	return out, true
}

func connectionForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	for _, key := range []string{"connection", "tool", "level"} {
		value, ok := args[key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out[key] = text
	}
	return out, true
}

func settingsForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	for _, key := range []string{"name", "theme", "email", "invite"} {
		value, ok := args[key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out[key] = text
	}
	return out, true
}

func chatForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	if value, ok := args["thread"]; ok {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out["thread"] = text
	}
	return out, true
}

func approvalSchema(spec approvetool.Spec) map[string]any {
	return map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"card": map[string]any{"type": "string", "description": "Tool name, words on the card, or the card id. Leave it out when only one approval is waiting."},
			},
		},
	}
}

func approvalForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	if value, ok := args["card"]; ok {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out["card"] = text
	}
	return out, true
}

func editForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, false
	}
	out := map[string]any{}
	for _, key := range []string{"name", "title", "job", "instructions", "color", "model"} {
		value, ok := args[key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out[key] = text
	}
	if value, ok := args["delegate"]; ok {
		flag, ok := value.(bool)
		if !ok {
			return nil, false
		}
		out["delegate"] = flag
	}
	return out, true
}

func groupForwardArgs(raw json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
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
		"name":        secretask.ToolSave,
		"description": "Ask to connect GitHub or DigitalOcean, or to save a new API key. For GitHub or DigitalOcean pass connection. For any other API pass name, an environment variable such as SERVICE_API_KEY, plus reason and an optional label. Leave connection empty for a named key. The value is saved on the host and is not returned. A successful save is available on the next turn. The result is saved, declined, expired, already_configured, or rejected.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"connection": map[string]any{"type": "string", "enum": []string{"github", "digitalocean"}},
				"name":       map[string]any{"type": "string", "description": "Environment name for a new API key. Capital letters, digits, and underscores."},
				"reason":     map[string]any{"type": "string"},
				"label":      map[string]any{"type": "string", "description": "Short title for the card."},
			},
		},
	}
}

func deleteSecretTool() map[string]any {
	return map[string]any{
		"name":        secretask.ToolDelete,
		"description": "Ask to delete a saved API key. Pass the same name used to save it, and a reason. The person approves on a card before the key is deleted. The result is deleted, declined, expired, or not_found.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":   map[string]any{"type": "string"},
				"reason": map[string]any{"type": "string"},
			},
			"required": []string{"name"},
		},
	}
}

func callSecret(base string, params json.RawMessage) (string, bool) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(params, &call)
	var args struct {
		Connection string `json:"connection"`
		Reason     string `json:"reason"`
		Name       string `json:"name"`
		Label      string `json:"label"`
	}
	_ = json.Unmarshal(call.Arguments, &args)
	req := egresscontract.SecretCall{Reason: args.Reason, Name: args.Name, Label: args.Label}
	switch call.Name {
	case secretask.ToolDelete:
		if !egresscontract.EnvName(strings.ToUpper(strings.TrimSpace(args.Name))) {
			return egresscontract.Rejected, true
		}
		req.Action = "delete"
		req.Name = strings.ToUpper(strings.TrimSpace(args.Name))
	case secretask.ToolSave:
		if strings.TrimSpace(args.Connection) == "" && strings.TrimSpace(args.Name) != "" {
			if !egresscontract.EnvName(strings.ToUpper(strings.TrimSpace(args.Name))) {
				return egresscontract.Rejected, true
			}
			req.Action = "save"
			req.Name = strings.ToUpper(strings.TrimSpace(args.Name))
			break
		}
		if !egresscontract.KnownProvider(args.Connection) {
			return egresscontract.Rejected, true
		}
		req.Action = "save"
		req.Connection = args.Connection
	default:
		return egresscontract.Rejected, true
	}
	body, err := postJSON(base+"/secret-request", req, 61*time.Minute)
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
