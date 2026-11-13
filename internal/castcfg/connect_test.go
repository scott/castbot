package castcfg

import (
	"strings"
	"testing"

	"github.com/scott/castbot/internal/connect"
)

func TestShellPanelMatchesBuiltinTools(t *testing.T) {
	panel, ok := connect.PanelLookup(connect.Shell)
	if !ok {
		t.Fatal("shell")
	}
	tools := Tools()
	if len(panel.Tools) != len(tools) {
		t.Fatalf("panel %d builtin %d", len(panel.Tools), len(tools))
	}
	for i, tool := range tools {
		if panel.Tools[i].Name != tool.Name || panel.Tools[i].Write != tool.Write {
			t.Fatalf("panel %#v builtin %#v", panel.Tools[i], tool)
		}
	}
}

func TestRequestSecretPermissionRoundTrips(t *testing.T) {
	raw := "name: Atlas\nbrain:\n  model: demo\n  instructions_file: instructions.md\ntools:\n  mcp:\n    - request_secret\nharness:\n  permissions:\n    mcp: ask\n    request_secret: always-allow\n    get_me: always-allow\n    create_issue: ask\n"
	spec, err := ParseYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Permissions[connect.RequestTool] != "always-allow" || spec.Permissions["mcp"] != "ask" || spec.Permissions["create_issue"] != "ask" {
		t.Fatalf("%#v", spec.Permissions)
	}
	out, err := RenderYAML(spec)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseYAML(string(out))
	if err != nil || !Agree(spec, again) {
		t.Fatalf("round trip %v\n%s", err, out)
	}
	leaky := "name: Atlas\nrequest_secret: test-connection-value\nharness:\n  permissions:\n    request_secret: test-connection-value\n"
	parsed, err := ParseYAML(leaky)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Permissions[connect.RequestTool] == "test-connection-value" || strings.Contains(string(parsed.Kept), "test-connection-value") {
		t.Fatalf("secret survived %#v %s", parsed.Permissions, parsed.Kept)
	}
}
