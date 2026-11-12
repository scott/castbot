package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPClientAllowsDecisionDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{}}`))
	}))
	defer srv.Close()
	c := NewHTTP(srv.URL)
	rt, ok := c.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport %T", c.client.Transport)
	}
	if rt.ResponseHeaderTimeout < 45*time.Second {
		t.Fatalf("response header timeout %s is shorter than the 45s decision deadline", rt.ResponseHeaderTimeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Respond(ctx, "req-1", true); err != nil {
		t.Fatal(err)
	}
}
