package agentfile

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGitHub(t *testing.T) {
	ok, err := ParseGitHub("https://github.com/acme/helper/tree/main/agents/docs")
	if err != nil || ok.Owner != "acme" || ok.Name != "helper" || ok.Ref != "" || strings.Join(ok.Unresolved, "/") != "main/agents/docs" {
		t.Fatalf("%+v %v", ok, err)
	}
	slashed, err := ParseGitHub("https://github.com/acme/helper/tree/feature/x/agents?ref=feature/x")
	if err != nil || slashed.Ref != "feature/x" || slashed.Subpath != "agents" {
		t.Fatalf("%+v %v", slashed, err)
	}
	withRef, err := ParseGitHub("https://github.com/acme/helper?ref=v1.2")
	if err != nil || withRef.Ref != "v1.2" || withRef.Subpath != "" {
		t.Fatalf("%+v %v", withRef, err)
	}
	plain, err := ParseGitHub("https://github.com/acme/helper.git")
	if err != nil || plain.Owner != "acme" || plain.Name != "helper" || plain.Ref != "" || plain.Subpath != "" {
		t.Fatalf("%+v %v", plain, err)
	}
	bad := []string{
		"http://github.com/acme/helper",
		"https://user:pass@github.com/acme/helper",
		"https://github.com.evil.example/acme/helper",
		"https://evil.example/acme/helper",
		"https://github.com@evil.example/acme/helper",
		"https://github.com/acme/helper/blob/main/agent.yaml",
		"ftp://github.com/acme/helper",
		"https://github.com/acme/../helper",
	}
	for _, raw := range bad {
		if _, err := ParseGitHub(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestReadArchiveSafety(t *testing.T) {
	good := tarGz(map[string]string{
		"helper/agent.yaml":      "name: Atlas\n",
		"helper/instructions.md": "Be concise.\n",
		"helper/.env":            "API_KEY=super-secret-value\n",
		"helper/notes.txt":       "left out\n",
	})
	folder, err := ReadArchive(good, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(folder.YAML, "name: Atlas") || !strings.Contains(folder.Instructions, "Be concise") {
		t.Fatalf("folder %+v", folder)
	}
	if strings.Contains(folder.YAML, "super-secret-value") || strings.Contains(folder.Instructions, "super-secret-value") {
		t.Fatal("secret was imported")
	}
	if len(folder.Skipped) != 1 || !strings.HasSuffix(folder.Skipped[0], ".env") {
		t.Fatalf("skipped %#v", folder.Skipped)
	}
	if _, err := ReadArchive(tarFile(map[string]string{"../agent.yaml": "name: x\n"}), ""); !IsTraversal(err) {
		t.Fatal(err)
	}
	if _, err := ReadArchive(tarFile(map[string]string{"/etc/passwd": "x"}), ""); !IsTraversal(err) {
		t.Fatal(err)
	}
	link := tarHeader("link", tar.TypeSymlink)
	if _, err := ReadArchive(tarRaw(link, nil), ""); !IsSymlink(err) {
		t.Fatal(err)
	}
	many := map[string]string{}
	for i := 0; i < 201; i++ {
		many[string(rune('a'+(i%26)))+string(rune('a'+((i/26)%26)))+".txt"] = "x"
	}
	many["agent.yaml"] = "name: x\n"
	if _, err := ReadArchive(tarFile(many), ""); !IsTooMany(err) {
		t.Fatal(err)
	}
	big := tarFile(map[string]string{"agent.yaml": strings.Repeat("a", (1<<20)+1)})
	if _, err := ReadArchive(big, ""); !IsOversize(err) {
		t.Fatal(err)
	}
	if _, err := ReadArchive(bytes.Repeat([]byte{'x'}, (4<<20)+1), ""); !IsOversize(err) {
		t.Fatal(err)
	}
	if _, err := ReadArchive(zipSymlink(), ""); !IsSymlink(err) {
		t.Fatal(err)
	}
}

func TestFetchRejectsARedirectOffGitHub(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/nope", http.StatusFound)
	}))
	defer src.Close()
	_, err := Fetch(context.Background(), nil, src.URL, Repo{Owner: "acme", Name: "helper"})
	if err == nil {
		t.Fatal("followed a redirect off github")
	}
}

func tarGz(files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	writeTar(tw, files)
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func tarFile(files map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTar(tw, files)
	tw.Close()
	return buf.Bytes()
}

func writeTar(tw *tar.Writer, files map[string]string) {
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		tw.WriteHeader(hdr)
		tw.Write([]byte(body))
	}
}

func tarHeader(name string, flag byte) *tar.Header {
	return &tar.Header{Name: name, Mode: 0o777, Size: 0, Typeflag: flag, Linkname: "agent.yaml"}
}

func tarRaw(hdr *tar.Header, body []byte) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(hdr)
	if len(body) > 0 {
		tw.Write(body)
	}
	tw.Close()
	return buf.Bytes()
}

func TestPaxHeaderIsNotALink(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	global := &tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "abc123"}}
	if err := tw.WriteHeader(global); err != nil {
		t.Fatal(err)
	}
	body := []byte("name: Atlas\nbrain:\n  instructions_file: prompts/system.md\n")
	if err := tw.WriteHeader(&tar.Header{Name: "repo-abc/agent.yaml", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	prompt := []byte("From the named file.\n")
	if err := tw.WriteHeader(&tar.Header{Name: "repo-abc/prompts/system.md", Mode: 0o644, Size: int64(len(prompt)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(prompt); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	folder, err := ReadArchive(buf.Bytes(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(folder.Instructions, "From the named file.") {
		t.Fatalf("instructions %q", folder.Instructions)
	}
	hard := tarHeader("hard", tar.TypeLink)
	if _, err := ReadArchive(tarRaw(hard, nil), ""); !IsSymlink(err) {
		t.Fatal(err)
	}
}

func TestSubpathIsAnchored(t *testing.T) {
	raw := tarFile(map[string]string{
		"repo-abc/agents/helper/agent.yaml":      "name: Good\n",
		"repo-abc/agents/helper/instructions.md": "ok\n",
		"repo-abc/evil/agents/helper/agent.yaml": "name: Evil\n",
	})
	folder, err := ReadArchive(raw, "agents/helper")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(folder.YAML, "Evil") || !strings.Contains(folder.YAML, "Good") {
		t.Fatalf("yaml %s", folder.YAML)
	}
}

func TestSecretNamesAreSkipped(t *testing.T) {
	files := map[string]string{
		"repo/agent.yaml":                "name: Atlas\n",
		"repo/instructions.md":           "ok\n",
		"repo/.envrc":                    "secret\n",
		"repo/.npmrc":                    "secret\n",
		"repo/.pypirc":                   "secret\n",
		"repo/id_rsa_prod":               "secret\n",
		"repo/service-account-prod.json": "secret\n",
		"repo/credentials-prod.json":     "secret\n",
		"repo/prod.env":                  "secret\n",
		"repo/app.keystore":              "secret\n",
	}
	folder, err := ReadArchive(tarFile(files), "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(folder.Skipped, " ")
	for _, name := range []string{".envrc", ".npmrc", ".pypirc", "id_rsa_prod", "service-account-prod.json", "credentials-prod.json", "prod.env", "app.keystore"} {
		if !strings.Contains(got, name) {
			t.Fatalf("missing %s in %s", name, got)
		}
	}
}

func TestGitArchiveRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=castbot-dev", "GIT_AUTHOR_EMAIL=castbot-dev@users.noreply.github.com", "GIT_COMMITTER_NAME=castbot-dev", "GIT_COMMITTER_EMAIL=castbot-dev@users.noreply.github.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s", strings.Join(args, " "), out)
		}
	}
	run("init")
	run("config", "user.email", "castbot-dev@users.noreply.github.com")
	run("config", "user.name", "castbot-dev")
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("name: Archived\nbrain:\n  instructions_file: instructions.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "instructions.md"), []byte("From git archive.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "agent.yaml", "instructions.md")
	run("commit", "-m", "agent")
	cmd := exec.Command("git", "archive", "--format=tar.gz", "--prefix=repo-abc123/", "HEAD")
	cmd.Dir = dir
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	folder, err := ReadArchive(raw, "")
	if err != nil {
		t.Fatalf("upload %v", err)
	}
	if !strings.Contains(folder.Instructions, "From git archive.") || !strings.Contains(folder.YAML, "Archived") {
		t.Fatalf("folder %+v", folder)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/acme/helper/tar.gz/") {
			http.NotFound(w, r)
			return
		}
		w.Write(raw)
	}))
	defer srv.Close()
	body, err := Fetch(context.Background(), srv.Client(), srv.URL, Repo{Owner: "acme", Name: "helper", Ref: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	folder, err = ReadArchive(body, "")
	if err != nil {
		t.Fatalf("github %v", err)
	}
	if !strings.Contains(folder.Instructions, "From git archive.") {
		t.Fatal(folder.Instructions)
	}
}

func TestResolveSlashedRef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "matching-refs/heads/") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"ref":"refs/heads/feature"},{"ref":"refs/heads/feature/x"}]`))
			return
		}
		if strings.Contains(r.URL.Path, "matching-refs/tags/") {
			w.Write([]byte(`[]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	repo, err := ParseGitHub("https://github.com/acme/helper/tree/feature/x/agents")
	if err != nil {
		t.Fatal(err)
	}
	body, err := Fetch(context.Background(), srv.Client(), srv.URL, repo)
	if err == nil {
		t.Fatal("archive was served")
	}
	_ = body
	resolved, err := resolveRef(context.Background(), srv.Client(), srv.URL, repo)
	if err != nil || resolved.Ref != "feature/x" || resolved.Subpath != "agents" {
		t.Fatalf("%+v %v", resolved, err)
	}
}

func TestRedirectToHTTPIsRefused(t *testing.T) {
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://codeload.github.com/acme/helper/tar.gz/HEAD", http.StatusFound)
	}))
	defer src.Close()
	_, err := Fetch(context.Background(), nil, src.URL, Repo{Owner: "acme", Name: "helper"})
	if err == nil {
		t.Fatal("followed an http redirect")
	}
}

func zipSymlink() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "agent.yaml"}
	h.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		return nil
	}
	w.Write([]byte("other"))
	zw.Close()
	return buf.Bytes()
}
