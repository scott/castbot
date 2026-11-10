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
	"strings"
	"testing"
)

func TestParseGitHub(t *testing.T) {
	ok, err := ParseGitHub("https://github.com/acme/helper/tree/main/agents/docs")
	if err != nil || ok.Owner != "acme" || ok.Name != "helper" || ok.Ref != "main" || ok.Subpath != "agents/docs" {
		t.Fatalf("%+v %v", ok, err)
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
