package connect

import (
	"sort"
	"strings"
)

// ShimCommand is the guest binary the image slice installs. It talks to
// castlet on loopback and never receives a vault value.
const ShimCommand = "/usr/local/bin/castbot-mcp"

// ShimYAML is an MCP server file: command and args only, no environment.
func ShimYAML(connection string) (string, bool) {
	switch connection {
	case RequestTool, GitHub, DigitalOcean:
		return "command: " + ShimCommand + "\nargs:\n  - " + connection + "\n", true
	default:
		return "", false
	}
}

// ShimFiles are the bundle files for request_secret plus each enabled connection.
func ShimFiles(enabled []string) map[string]string {
	out := map[string]string{}
	if yml, ok := ShimYAML(RequestTool); ok {
		out["mcp/request_secret.yaml"] = yml
	}
	for _, id := range enabled {
		yml, ok := ShimYAML(id)
		if !ok {
			continue
		}
		out["mcp/"+id+".yaml"] = yml
	}
	return out
}

// ServerNames is the tools.mcp list: request_secret, then enabled connections,
// then any kept name that is not one of ours.
func ServerNames(existing, enabled []string) []string {
	ours := map[string]bool{RequestTool: true, GitHub: true, DigitalOcean: true}
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || strings.ContainsAny(name, " \t\r\n/\\") {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range existing {
		if ours[name] {
			continue
		}
		add(name)
	}
	add(RequestTool)
	ordered := append([]string(nil), enabled...)
	sort.Strings(ordered)
	for _, name := range ordered {
		if ours[name] && name != RequestTool {
			add(name)
		}
	}
	return out
}

// PermissionsFor returns the harness keys a connection set should write.
// Read tools are always-allow. Write tools are ask. mcp: ask covers new
// names only when a connection is on. request_secret is always-allow so
// the card is the only prompt.
func PermissionsFor(enabled []string) map[string]string {
	out := map[string]string{RequestTool: "always-allow"}
	if len(enabled) == 0 {
		return out
	}
	out["mcp"] = "ask"
	for _, id := range enabled {
		for _, tool := range Tools(id) {
			if tool.Write {
				out[tool.Name] = "ask"
			} else {
				out[tool.Name] = "always-allow"
			}
		}
	}
	return out
}
