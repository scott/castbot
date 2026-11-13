package connect

import (
	"strings"
	"testing"
)

func TestRequestHashBindsBodyAndQuery(t *testing.T) {
	a := RequestHash("POST", "https://api.github.com/repos/octo/demo/issues", []byte(`{"title":"harmless"}`))
	b := RequestHash("post", "https://api.github.com/repos/octo/demo/issues", []byte(`{"title":"harmless"}`))
	swapped := RequestHash("POST", "https://api.github.com/repos/octo/demo/issues", []byte(`{"title":"swapped"}`))
	queried := RequestHash("POST", "https://api.github.com/repos/octo/demo/issues?x=1", []byte(`{"title":"harmless"}`))
	if a != b || a == swapped || a == queried || len(a) != 64 {
		t.Fatal("hash")
	}
	sum := RequestSummary("POST", "https://api.github.com/repos/octo/demo/issues", []byte(`{"title":"harmless"}`))
	if !strings.Contains(sum, "POST /repos/octo/demo/issues") || !strings.Contains(sum, "harmless") {
		t.Fatal(sum)
	}
	withQuery := RequestSummary("POST", "https://api.github.com/repos/octo/demo/issues?x=git-upload-pack", []byte(`{"title":"harmless"}`))
	if !strings.Contains(withQuery, "?x=git-upload-pack") || !strings.Contains(withQuery, "harmless") {
		t.Fatal(withQuery)
	}
	secret := RequestSummary("POST", "https://api.github.com/user", []byte("github"+"_pat_"+"abcdefghij"))
	if strings.Contains(secret, "abcdefghij") || !strings.Contains(secret, "[redacted]") {
		t.Fatal("summary secret")
	}
}

func TestMCPHashCanonicalArguments(t *testing.T) {
	left, err := MCPHash("create_issue", []byte(`{"title":"harmless","body":"n"}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := MCPHash("create_issue", []byte(`{"body":"n","title":"harmless"}`))
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := MCPHash("create_issue", []byte(`{"title":"swapped"}`))
	if err != nil {
		t.Fatal(err)
	}
	if left != right || left == swapped {
		t.Fatal("mcp hash")
	}
	sum := MCPSummary("create_issue", []byte(`{"title":"harmless"}`))
	if !strings.Contains(sum, "create_issue") || !strings.Contains(sum, "harmless") {
		t.Fatal(sum)
	}
}
