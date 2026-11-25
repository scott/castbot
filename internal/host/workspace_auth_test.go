package host

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

func TestWorkspaceListReadsACodingCheckout(t *testing.T) {
	const catchAll = "issuetest/docs-site/src/pages/[...slug].astro"
	var b strings.Builder
	b.WriteByte('[')
	const n = 40000
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":"src/file-`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`.ts","size":1}`)
	}
	b.WriteString(`,{"name":"` + catchAll + `","size":12}]`)
	raw := b.String()
	if len(raw) <= 1<<20 {
		t.Fatalf("fixture %d bytes", len(raw))
	}
	var gotPath string
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/castlet/files" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, raw)
			return
		}
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), "do-token")
	listed, err := m.ListWorkspace(context.Background(), edge.URL, "agent-token")
	if err != nil || len(listed) != n+1 {
		t.Fatalf("list %d %v", len(listed), err)
	}
	if listed[n].Name != catchAll || listed[n].Size != 12 {
		t.Fatalf("last %#v", listed[n])
	}
	rc, err := m.ReadWorkspace(context.Background(), edge.URL, "agent-token", catchAll)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if !strings.Contains(gotPath, "%5B...slug%5D.astro") {
		t.Fatalf("path %s", gotPath)
	}
	dec, err := url.PathUnescape(gotPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range strings.Split(dec, "/") {
		if part == "." || part == ".." {
			t.Fatalf("segment in %s", gotPath)
		}
	}
}

func TestWorkspaceListStopsWhenTheBodyIsTooBig(t *testing.T) {
	raw := `[{"name":"notes.txt","size":1}]`
	if _, err := parseWorkspaceList(strings.NewReader(raw+"x"), int64(len(raw))); !errors.Is(err, ErrGuest) {
		t.Fatal(err)
	}
	listed, err := parseWorkspaceList(strings.NewReader(raw), int64(len(raw)))
	if err != nil || len(listed) != 1 || listed[0].Name != "notes.txt" {
		t.Fatalf("%#v %v", listed, err)
	}
	mixed := `[{"name":"docs/../../secret","size":1},{"name":"app/pages/[...slug].tsx","size":2}]`
	listed, err = parseWorkspaceList(strings.NewReader(mixed), int64(len(mixed)))
	if err != nil || len(listed) != 1 || listed[0].Name != "app/pages/[...slug].tsx" || listed[0].Size != 2 {
		t.Fatalf("%#v %v", listed, err)
	}
}

func TestCleanNameAllowsCatchAll(t *testing.T) {
	const catchAll = "issuetest/docs-site/src/pages/[...slug].astro"
	got, err := cleanName(catchAll)
	if err != nil || got != catchAll {
		t.Fatalf("%q %v", got, err)
	}
	for _, name := range []string{"../etc/passwd", "docs/../../secret", "..", "foo/..", "foo/./bar"} {
		if _, err := cleanName(name); err == nil {
			t.Fatalf("allowed %q", name)
		}
	}
}
