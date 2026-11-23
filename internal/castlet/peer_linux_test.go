//go:build linux

package castlet

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCallerRowIsNotTheServerRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	server := hexIPv4(net.ParseIP("127.0.0.1")) + ":" + hexPort("8090")
	client := hexIPv4(net.ParseIP("127.0.0.1")) + ":" + hexPort("12345")
	own := hexIPv4(net.ParseIP("127.0.0.1")) + ":" + hexPort("12346")
	other := os.Getuid() + 1
	if other == os.Getuid() {
		other = 2
	}
	body := "sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n" +
		fmt.Sprintf(" 0: %s %s 01 00000000:00000000 00:00000000 00000000 %d 0 1\n", server, client, os.Getuid()) +
		fmt.Sprintf(" 1: %s %s 01 00000000:00000000 00:00000000 00000000 %d 0 2\n", client, server, other) +
		" 2: 0100007F:0001 0100007F:0002 01 00000000:00000000 00:00000000 00000000 zz 0 3\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := procTCP
	procTCP = path
	t.Cleanup(func() { procTCP = prev })

	uid, ok := uidFromProc(path, client, server)
	if !ok || uid != other {
		t.Fatalf("caller uid %d ok %v", uid, ok)
	}
	if _, ok := uidFromProc(path, "0100007F:0001", server); ok {
		t.Fatal("missing row was accepted")
	}
	if _, ok := uidFromProc(path, "0100007F:0001", "0100007F:0002"); ok {
		t.Fatal("unreadable uid was accepted")
	}

	req := httptestRequest("127.0.0.1:12345", "127.0.0.1:8090")
	if sameUserPeer(req) {
		t.Fatal("another user was allowed")
	}

	body = "sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n" +
		fmt.Sprintf(" 0: %s %s 01 00000000:00000000 00:00000000 00000000 %d 0 4\n", own, server, os.Getuid())
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	req = httptestRequest("127.0.0.1:12346", "127.0.0.1:8090")
	if !sameUserPeer(req) {
		t.Fatal("the runtime user was refused")
	}

	procTCP = filepath.Join(dir, "missing")
	if sameUserPeer(req) {
		t.Fatal("a missing table was allowed")
	}
}

func httptestRequest(remote, local string) *http.Request {
	req := (&http.Request{RemoteAddr: remote}).WithContext(context.Background())
	host, port, _ := net.SplitHostPort(local)
	addr := &net.TCPAddr{IP: net.ParseIP(host), Port: atoiPort(port)}
	return req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, addr))
}

func atoiPort(port string) int {
	n := 0
	for _, c := range port {
		n = n*10 + int(c-'0')
	}
	return n
}
