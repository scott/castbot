package connect

import (
	"net"
	"strings"
	"testing"
)

func TestAllowlistRefusesTricks(t *testing.T) {
	ok := []struct{ conn, raw string }{
		{GitHub, "https://api.github.com/user"},
		{GitHub, "https://api.github.com./user"},
		{GitHub, "https://github.com/owner/repo.git/info/refs?service=git-upload-pack"},
		{GitHub, "https://api.github.com:443/user"},
		{DigitalOcean, "https://api.digitalocean.com/v2/account"},
	}
	for _, c := range ok {
		if _, err := CheckURL(c.conn, c.raw); err != nil {
			t.Fatalf("want allow %s", c.raw)
		}
	}
	bad := []string{
		"http://api.github.com/user",
		"https://user:secret@api.github.com/user",
		"https://1.2.3.4/user",
		"https://[2001:db8::1]/user",
		"https://api.github.com:8443/user",
		"https://api.github.com.evil.example/user",
		"https://evil.example/user",
		"https://github.com.evil.example/user",
		"not a url",
		"",
	}
	for _, raw := range bad {
		if _, err := CheckURL(GitHub, raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if _, err := CheckURL("custom", "https://api.github.com/user"); err == nil {
		t.Fatal("custom connection")
	}
}

func TestPublicIP(t *testing.T) {
	refuse := []string{
		"127.0.0.1", "10.1.1.1", "192.168.1.1", "172.16.0.1",
		"169.254.169.254", "100.64.0.1", "100.127.255.255",
		"0.0.0.0", "0.1.2.3", "224.0.0.1", "255.255.255.255",
		"::1", "fc00::1", "fe80::1", "::",
	}
	for _, raw := range refuse {
		if PublicIP(net.ParseIP(raw)) {
			t.Fatalf("public %s", raw)
		}
	}
	allow := []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888"}
	for _, raw := range allow {
		if !PublicIP(net.ParseIP(raw)) {
			t.Fatalf("refused %s", raw)
		}
	}
	if PublicIP(nil) {
		t.Fatal("nil")
	}
}

func TestMutatingAndMethod(t *testing.T) {
	reads := []string{
		"GET https://api.github.com/user",
		"GET https://github.com/octo/demo.git/info/refs?service=git-upload-pack",
		"POST https://github.com/octo/demo.git/git-upload-pack",
		"POST https://github.com/octo/demo/git-upload-pack",
		"POST https://github.com/octo/demo.git/git-upload-pack?extra=1",
		"HEAD https://api.digitalocean.com/v2/account",
	}
	writes := []string{
		"POST https://api.github.com/repos/octo/demo/issues",
		"POST https://api.github.com/repos/octo/demo/issues?x=git-upload-pack",
		"POST https://github.com/octo/demo/issues?x=git-upload-pack",
		"POST https://api.digitalocean.com/v2/droplets?note=git-upload-pack",
		"POST https://api.github.com/repos/octo/git-upload-pack",
		"POST https://github.com/octo/demo.git/git-upload-pack/extra",
		"POST https://github.com/octo/demo.git/info/refs?service=git-upload-pack",
		"GET https://github.com/octo/demo.git/info/refs?service=git-receive-pack",
		"GET https://github.com/octo/demo.git/info/refs?service=git-receive-pack&other=1",
		"GET https://github.com/octo/demo.git/git-receive-pack",
		"POST https://github.com/octo/demo.git/git-receive-pack",
		"GET https://github.com/octo/demo/git-receive-pack",
		"DELETE https://api.digitalocean.com/v2/droplets/1",
	}
	for _, item := range reads {
		method, raw, _ := strings.Cut(item, " ")
		if Mutating(method, raw) {
			t.Fatalf("read classified as write: %s", item)
		}
	}
	for _, item := range writes {
		method, raw, _ := strings.Cut(item, " ")
		if !Mutating(method, raw) {
			t.Fatalf("write classified as read: %s", item)
		}
	}
	if MethodOK("TRACE") || MethodOK("CONNECT") || MethodOK("OPTIONS") || !MethodOK("PATCH") {
		t.Fatal("method set")
	}
}

func TestUpstreamHosts(t *testing.T) {
	api, err := APIURL(GitHub, "/user", "per_page=1")
	if err != nil || api != "https://api.github.com/user?per_page=1" {
		t.Fatalf("github api %s %v", api, err)
	}
	git, err := GitURL(GitHub, "/octo/demo.git/git-upload-pack", "service=git-upload-pack")
	if err != nil || git != "https://github.com/octo/demo.git/git-upload-pack?service=git-upload-pack" {
		t.Fatalf("git %s %v", git, err)
	}
	do, err := APIURL(DigitalOcean, "/v2/account", "")
	if err != nil || do != "https://api.digitalocean.com/v2/account" {
		t.Fatalf("do %s %v", do, err)
	}
	collapsed, err := APIURL(GitHub, "//evil.example/user", "")
	if err != nil || collapsed != "https://api.github.com/evil.example/user" {
		t.Fatalf("collapsed %s %v", collapsed, err)
	}
	for _, raw := range []string{"https://evil.example/user", "/../etc", "/a/../../b", "/a%2f%2e%2e"} {
		if _, err := APIURL(GitHub, raw, ""); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := GitURL(DigitalOcean, "/v2/account", ""); err == nil {
		t.Fatal("digitalocean git")
	}
	if _, err := APIURL(GitHub, "/user", "next=https://evil.example"); err == nil {
		t.Fatal("query host")
	}
}
