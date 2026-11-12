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
