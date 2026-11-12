package host

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEdgeClientAllowsDecisionDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{}}`))
	}))
	defer srv.Close()
	m, err := New(Options{Token: "machine-token-for-tests", BaseURL: srv.URL, InsecureEdge: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.edge.Timeout < 45*time.Second {
		t.Fatalf("edge timeout %s is shorter than the 45s decision deadline", m.edge.Timeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Approve(ctx, srv.URL, "agent-token", "req-1", true); err != nil {
		t.Fatal(err)
	}
}
