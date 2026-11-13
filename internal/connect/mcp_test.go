package connect

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAdapterListsToolsWithoutAToken(t *testing.T) {
	const token = "test-connection-value"
	out, err := HandleRPC(context.Background(), GitHub, RPC{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), token) || !strings.Contains(string(out), "get_me") || !strings.Contains(string(out), "create_issue") {
		t.Fatal(string(out))
	}
	_, err = HandleRPC(context.Background(), GitHub, RPC{
		ID: json.RawMessage(`2`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"create_issue","arguments":{"owner":"octo","repo":"demo","title":"hi","token":"` + token + `"}}`),
	}, false, func(ctx context.Context, call Call) (Result, error) {
		t.Fatal("write ran without approval")
		return Result{}, nil
	})
	if err != ErrDenied {
		t.Fatalf("write %v", err)
	}
	var saw Call
	out, err = HandleRPC(context.Background(), GitHub, RPC{
		ID: json.RawMessage(`3`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"get_me"}`),
	}, false, func(ctx context.Context, call Call) (Result, error) {
		saw = call
		return Result{Status: 200, Body: []byte(`{"login":"octo"}`)}, nil
	})
	if err != nil || saw.URL != "https://api.github.com/user" || saw.Method != "GET" || strings.Contains(string(out), token) {
		t.Fatalf("%s %v %+v", out, err, saw)
	}
	out, err = HandleRPC(context.Background(), GitHub, RPC{
		ID: json.RawMessage(`4`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"create_issue","arguments":{"owner":"octo","repo":"demo","title":"hi","token":"` + token + `"}}`),
	}, true, func(ctx context.Context, call Call) (Result, error) {
		saw = call
		return Result{Status: 201, Body: []byte(`{"ok":true}`)}, nil
	})
	if err != nil || strings.Contains(string(saw.Body), token) || !strings.Contains(string(saw.Body), "hi") || saw.Method != "POST" {
		t.Fatalf("body %s err %v", saw.Body, err)
	}
	if _, err := HandleRPC(context.Background(), RequestTool, RPC{Method: "tools/call", Params: json.RawMessage(`{"name":"request_secret"}`)}, true, nil); !IsRequest(err) {
		t.Fatal(err)
	}
}

func TestScrubReason(t *testing.T) {
	prefix := "github" + "_pat_"
	got := ScrubReason("need " + prefix + "abcdefghij to ship\nplease")
	if strings.Contains(got, prefix) || strings.Contains(got, "\n") || !strings.Contains(got, "[redacted]") {
		t.Fatal(got)
	}
	long := strings.Repeat("a", 400)
	if len(ScrubReason(long)) > 240 {
		t.Fatal("length")
	}
}

func TestShimHasNoToken(t *testing.T) {
	files := ShimFiles([]string{GitHub, DigitalOcean})
	for name, body := range files {
		if strings.Contains(body, "env:") || strings.Contains(body, "TOKEN") || !strings.Contains(body, ShimCommand) {
			t.Fatalf("%s %s", name, body)
		}
	}
	names := ServerNames([]string{"helper", GitHub}, []string{DigitalOcean, GitHub})
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "helper") || !strings.Contains(joined, RequestTool) || !strings.Contains(joined, GitHub) {
		t.Fatal(joined)
	}
	perms := PermissionsFor([]string{GitHub})
	if perms[RequestTool] != "always-allow" || perms["mcp"] != "ask" || perms["get_me"] != "always-allow" || perms["create_issue"] != "ask" {
		t.Fatalf("%#v", perms)
	}
}
