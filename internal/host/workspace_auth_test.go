package host

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestWorkspaceMethodsSendEdgeAuth(t *testing.T) {
	const machine = "do-token"
	const agent = "agent-token"
	var mu sync.Mutex
	var hits [][3]string
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, [3]string{r.Method + " " + r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Cast-Authorization")})
		mu.Unlock()
		if r.URL.Path == "/castlet/files" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[{"name":"notes.txt","size":1}]`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), machine)
	ctx := context.Background()
	if _, err := m.ListWorkspace(ctx, edge.URL, agent); err != nil {
		t.Fatal(err)
	}
	rc, err := m.ReadWorkspace(ctx, edge.URL, agent, "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if err := m.WriteWorkspace(ctx, edge.URL, agent, "notes.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits %#v", hits)
	}
	for _, hit := range hits {
		if hit[1] != "Bearer "+machine || hit[2] != "Bearer "+agent {
			t.Fatalf("%s Authorization %q X-Cast-Authorization %q", hit[0], hit[1], hit[2])
		}
	}
}

func TestWorkspaceMethodsRejectEscapes(t *testing.T) {
	var hits int
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.RawPath, "..") {
			t.Errorf("requested %s", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"name":"notes.txt","size":1},{"name":"../etc/passwd","size":2},{"name":"/etc/passwd","size":3},{"name":"..%2f..%2fetc","size":4},{"name":"a\u0000b","size":5}]`)
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), "do-token")
	ctx := context.Background()
	listed, err := m.ListWorkspace(ctx, edge.URL, "agent-token")
	if err != nil || len(listed) != 1 || listed[0].Name != "notes.txt" {
		t.Fatalf("list %#v %v", listed, err)
	}
	before := hits
	for _, name := range []string{"../etc/passwd", "/etc/passwd", "..%2f..%2fetc", "notes\x00.txt", "docs/../../secret"} {
		if _, err := m.ReadWorkspace(ctx, edge.URL, "agent-token", name); err == nil {
			t.Fatalf("read %q", name)
		}
		if err := m.WriteWorkspace(ctx, edge.URL, "agent-token", name, strings.NewReader("x")); err == nil {
			t.Fatalf("write %q", name)
		}
	}
	if hits != before {
		t.Fatalf("escape made %d calls", hits-before)
	}
}
