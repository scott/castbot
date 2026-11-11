package host

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamRedirectDropsAuthorization(t *testing.T) {
	var auth, cast []string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		cast = append(cast, r.Header.Get("X-Cast-Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer evil.Close()
	target := strings.Replace(evil.URL, "127.0.0.1", "localhost", 1)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/castlet/memory", http.StatusTemporaryRedirect)
	}))
	defer good.Close()
	m := &MicroVM{insecure: true}
	body, err := m.ExportMemory(context.Background(), good.URL, "T")
	if body != nil {
		io.Copy(io.Discard, body)
		body.Close()
	}
	if len(auth) != 0 || len(cast) != 0 {
		t.Fatalf("redirect target saw Authorization %q X-Cast-Authorization %q", auth, cast)
	}
	if err == nil {
		t.Fatal("redirect treated as success")
	}
}
