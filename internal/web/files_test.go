package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func TestStreamingUploadOverPostLimit(t *testing.T) {
	ip := "203.0.113.140:9"
	h, fake, agent := fileMachine(t, ip, "running")
	body := bytes.Repeat([]byte("a"), 2<<20)
	code, text, hdr := postFile(t, h, ip, agent.ID, "notes.txt", "multipart/form-data; boundary=not-real", body)
	if code != 200 {
		t.Fatalf("upload %d %s", code, text)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "application/json") {
		t.Fatal(hdr.Get("Content-Type"))
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "notes.txt" || got["status"] != "copied" {
		t.Fatalf("%#v", got)
	}
	blob := filepath.Join(h.srv.dataDir, "files")
	matches, _ := filepath.Glob(filepath.Join(blob, "*"))
	if len(matches) != 1 {
		t.Fatalf("blobs %#v", matches)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil || !bytes.Equal(raw, body) {
		t.Fatalf("blob %d %v", len(raw), err)
	}
	disk := fake.Disks["https://vm.example"].Files["uploads/notes.txt"]
	if !bytes.Equal(disk, body) {
		t.Fatalf("guest %d", len(disk))
	}
}

func TestStreamingDownloadChunks(t *testing.T) {
	ip := "203.0.113.141:9"
	h, fake, agent := fileMachine(t, ip, "running")
	payload := bytes.Repeat([]byte("b"), 100<<10)
	fake.SeedDisk("https://vm.example", nil, map[string][]byte{"docs/pricing.md": payload})
	if err := h.st.ObserveWorkspace(context.Background(), agent.UserID, agent.ID, []store.ListedFile{{Name: "docs/pricing.md", Size: int64(len(payload))}}); err != nil {
		t.Fatal(err)
	}
	cached, err := h.st.ListCached(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(cached) != 1 {
		t.Fatal(err)
	}
	res := getRaw(t, h, ip, "/agents/"+agent.ID+"/files/"+cached[0].ID+"/download")
	if res.code != 200 || res.writes < 2 || res.max > 32<<10 || !bytes.Equal(res.buf.Bytes(), payload) {
		t.Fatalf("code %d writes %d max %d len %d", res.code, res.writes, res.max, res.buf.Len())
	}
	if !strings.Contains(res.hdr.Get("Content-Disposition"), "attachment") {
		t.Fatal(res.hdr.Get("Content-Disposition"))
	}
	if !strings.Contains(res.hdr.Get("Content-Security-Policy"), "sandbox") || res.hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %#v", res.hdr)
	}
	calls, _ := fake.Snapshot()
	if !containsCall(calls, "read-file") {
		t.Fatalf("calls %#v", calls)
	}
}

func TestUploadMidStreamAbort(t *testing.T) {
	ip := "203.0.113.142:9"
	h, _, agent := fileMachine(t, ip, "running")
	req := h.rawReq(http.MethodPost, "/agents/"+agent.ID+"/uploads", &errAfter{left: 100}, ip)
	req.ContentLength = 4000
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-File-Name", "report.csv")
	req.Header.Set("X-CSRF-Token", h.csrf)
	code, text, _ := h.serve(req)
	if code != http.StatusBadRequest || !strings.Contains(text, "Nothing was saved.") {
		t.Fatalf("%d %s", code, text)
	}
	rows, err := h.st.ListUploads(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows %#v %v", rows, err)
	}
	matches, _ := filepath.Glob(filepath.Join(h.srv.dataDir, "files", "*"))
	if len(matches) != 0 {
		t.Fatalf("left %#v", matches)
	}
}

func TestUploadCap32MB(t *testing.T) {
	ip := "203.0.113.143:9"
	h, _, agent := fileMachine(t, ip, "running")
	code, text, _ := postFile(t, h, ip, agent.ID, "big.bin", "application/octet-stream", bytes.Repeat([]byte("c"), fileCap))
	if code != 200 || !strings.Contains(text, `"status":"copied"`) {
		t.Fatalf("32MB %d %s", code, text)
	}
	for _, n := range []int64{fileCap + 1, int64(412 * 1024 * 1024 / 10)} {
		req := h.rawReq(http.MethodPost, "/agents/"+agent.ID+"/uploads", strings.NewReader("x"), ip)
		req.ContentLength = n
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-File-Name", "huge.bin")
		req.Header.Set("X-CSRF-Token", h.csrf)
		code, text, _ := h.serve(req)
		if code != http.StatusRequestEntityTooLarge || !strings.Contains(text, tooLargeCopy) {
			t.Fatalf("len %d -> %d %s", n, code, text)
		}
	}
	rows, err := h.st.ListUploads(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(rows) != 1 || rows[0].Size != fileCap {
		t.Fatalf("rows %#v %v", rows, err)
	}
}

func TestUploadNameCollisionBeforeExtension(t *testing.T) {
	ip := "203.0.113.144:9"
	h, fake, agent := fileMachine(t, ip, "running")
	first := []byte("one")
	second := []byte("two")
	code, text, _ := postFile(t, h, ip, agent.ID, "report.csv", "application/octet-stream", first)
	if code != 200 || !strings.Contains(text, `"name":"report.csv"`) {
		t.Fatalf("first %d %s", code, text)
	}
	code, text, _ = postFile(t, h, ip, agent.ID, "report.csv", "application/octet-stream", second)
	if code != 200 || !strings.Contains(text, `"name":"report (2).csv"`) {
		t.Fatalf("second %d %s", code, text)
	}
	files := fake.Disks["https://vm.example"].Files
	if !bytes.Equal(files["uploads/report.csv"], first) || !bytes.Equal(files["uploads/report (2).csv"], second) {
		t.Fatalf("disk %#v", files)
	}
}

func TestHeldUploadDoesNotWake(t *testing.T) {
	ip := "203.0.113.145:9"
	h, fake, agent := fileMachine(t, ip, "paused")
	before, _ := fake.Snapshot()
	code, text, _ := postFile(t, h, ip, agent.ID, "notes.txt", "application/octet-stream", []byte("held"))
	if code != 200 || !strings.Contains(text, `"status":"held"`) || !strings.Contains(text, "Saved in castbot.") {
		t.Fatalf("%d %s", code, text)
	}
	after, _ := fake.Snapshot()
	if guestCall(before, after) {
		t.Fatalf("upload called the guest %#v", after)
	}
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "running", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	fake.SetMachineState("vm-files", "running")
	h.srv.fireAfterRun(context.Background(), agent.UserID, agent.ID)
	rows, err := h.st.ListUploads(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(rows) != 1 || rows[0].Status != "copied" {
		t.Fatalf("rows %#v %v", rows, err)
	}
	if !bytes.Equal(fake.Disks["https://vm.example"].Files["uploads/notes.txt"], []byte("held")) {
		t.Fatal("not copied")
	}
}

func TestOpeningFilesDoesNotWake(t *testing.T) {
	ip := "203.0.113.146:9"
	h, fake, agent := fileMachine(t, ip, "paused")
	if err := h.st.ObserveWorkspace(context.Background(), agent.UserID, agent.ID, []store.ListedFile{{Name: "docs/pricing.md", Size: 12}}); err != nil {
		t.Fatal(err)
	}
	before, edgeBefore := fake.Snapshot()
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=files", "", ip, nil)
	after, edgeAfter := fake.Snapshot()
	if code != 200 || !strings.Contains(body, "pricing.md") || !strings.Contains(body, "Opening Files does not wake it.") {
		t.Fatalf("%d %s", code, snippet(body))
	}
	if guestCall(before, after) || edgeAfter != edgeBefore {
		t.Fatalf("guest calls %#v edge %d", after, edgeAfter-edgeBefore)
	}
}

func TestWorkspaceDownloadWakes(t *testing.T) {
	ip := "203.0.113.147:9"
	h, fake, agent := fileMachine(t, ip, "paused")
	payload := []byte("pricing")
	fake.SeedDisk("https://vm.example", nil, map[string][]byte{"docs/pricing.md": payload})
	if err := h.st.ObserveWorkspace(context.Background(), agent.UserID, agent.ID, []store.ListedFile{{Name: "docs/pricing.md", Size: int64(len(payload))}}); err != nil {
		t.Fatal(err)
	}
	cached, err := h.st.ListCached(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(cached) != 1 {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/files/"+cached[0].ID+"/download", "", ip, nil)
	if code != 200 || body != "pricing" {
		t.Fatalf("%d %q", code, body)
	}
	calls, _ := fake.Snapshot()
	if !containsCall(calls, "wake") || !containsCall(calls, "read-file") {
		t.Fatalf("calls %#v", calls)
	}
	events, err := h.st.ListVMEvents(context.Background(), agent.UserID, agent.ID)
	if err != nil || !eventKind(events, store.EventWakeRequested) || !eventKind(events, store.EventWakeReady) {
		t.Fatalf("events %#v %v", events, err)
	}
}

func TestWorkspaceDownloadFailedWake(t *testing.T) {
	ip := "203.0.113.148:9"
	h, fake, agent := fileMachine(t, ip, "paused")
	delete(fake.Machines, "vm-files")
	if err := h.st.ObserveWorkspace(context.Background(), agent.UserID, agent.ID, []store.ListedFile{{Name: "docs/pricing.md", Size: 3}}); err != nil {
		t.Fatal(err)
	}
	cached, err := h.st.ListCached(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(cached) != 1 {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/files/"+cached[0].ID+"/download", "", ip, nil)
	if code != http.StatusBadGateway || !strings.Contains(body, "The machine is paused.") {
		t.Fatalf("%d %s", code, body)
	}
	events, err := h.st.ListVMEvents(context.Background(), agent.UserID, agent.ID)
	if err != nil || !eventKind(events, store.EventWakeRequested) || !eventKind(events, store.EventWakeFailed) || eventKind(events, store.EventWakeReady) {
		t.Fatalf("events %#v %v", events, err)
	}
}

func TestSeenTimeSurvivesPause(t *testing.T) {
	ip := "203.0.113.149:9"
	h, fake, agent := fileMachine(t, ip, "running")
	when := time.Date(2026, 11, 12, 19, 42, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return when })
	fake.SeedDisk("https://vm.example", nil, map[string][]byte{"docs/pricing.md": []byte("hi")})
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=files", "", ip, nil)
	if code != 200 || !strings.Contains(body, "seen 12:42 PM") {
		t.Fatalf("first %d %s", code, snippet(body))
	}
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "paused", "", ""); err != nil {
		t.Fatal(err)
	}
	fake.SetMachineState("vm-files", "paused")
	h.st.SetNow(func() time.Time { return when.Add(3 * time.Hour) })
	before, _ := fake.Snapshot()
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=files", "", ip, nil)
	after, _ := fake.Snapshot()
	if code != 200 || !strings.Contains(body, "seen 12:42 PM") || strings.Contains(body, "seen 3:42 PM") {
		t.Fatalf("paused %d %s", code, snippet(body))
	}
	if addedCall(before, after, "list-files") {
		t.Fatal("paused list called the guest")
	}
}

func TestUploadQueueOneAtATime(t *testing.T) {
	ip := "203.0.113.150:9"
	h, _, agent := fileMachine(t, ip, "running")
	hold := make(chan struct{})
	h.srv.SetUploadHold(hold)
	done := make(chan error, 2)
	go func() {
		code, text, _ := postFile(t, h, ip, agent.ID, "a.txt", "application/octet-stream", []byte("a"))
		if code != 200 {
			done <- errString(text)
			return
		}
		done <- nil
	}()
	waitLog(t, h, "acquired a.txt")
	go func() {
		code, text, _ := postFile(t, h, ip, agent.ID, "b.txt", "application/octet-stream", []byte("b"))
		if code != 200 {
			done <- errString(text)
			return
		}
		done <- nil
	}()
	time.Sleep(150 * time.Millisecond)
	if strings.Contains(strings.Join(h.srv.UploadLog(), "\n"), "acquired b.txt") {
		t.Fatalf("second started early %#v", h.srv.UploadLog())
	}
	close(hold)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	log := strings.Join(h.srv.UploadLog(), "\n")
	if !strings.Contains(log, "acquired a.txt") || !strings.Contains(log, "end a.txt") || !strings.Contains(log, "acquired b.txt") {
		t.Fatalf("log %s", log)
	}
	if strings.Index(log, "end a.txt") > strings.Index(log, "acquired b.txt") {
		t.Fatalf("overlapped %s", log)
	}
}

func TestNoFileDeleteRoute(t *testing.T) {
	ip := "203.0.113.151:9"
	h, _, agent := fileMachine(t, ip, "running")
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=files", "", ip, nil)
	if code != 200 || strings.Contains(body, "Delete file") || strings.Contains(body, ">Delete<") {
		t.Fatalf("delete control %d", code)
	}
	for _, path := range []string{
		"/agents/" + agent.ID + "/uploads",
		"/agents/" + agent.ID + "/files/anything",
		"/agents/" + agent.ID + "/files/anything/download",
	} {
		code, _, _ := h.do(http.MethodDelete, path, "", ip, nil)
		if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Fatalf("DELETE %s -> %d", path, code)
		}
	}
}

func TestUploadQuotaAndWarning(t *testing.T) {
	ip := "203.0.113.152:9"
	h, _, agent := fileMachine(t, ip, "running")
	warnAt := int64(uploadQuota*8/10 + 1<<20)
	if _, err := h.st.InsertUpload(context.Background(), agent.UserID, agent.ID, "", "big.bin", "", strings.Repeat("ab", 32), "workspace/uploads/big.bin", "stored", warnAt); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=files", "", ip, nil)
	if code != 200 || !strings.Contains(body, "This workspace is past 80% of its 2 GB upload space.") {
		t.Fatalf("warn %d", code)
	}
	if _, err := h.st.InsertUpload(context.Background(), agent.UserID, agent.ID, "", "rest.bin", "", strings.Repeat("cd", 32), "workspace/uploads/rest.bin", "stored", uploadQuota-warnAt); err != nil {
		t.Fatal(err)
	}
	code, text, _ := postFile(t, h, ip, agent.ID, "more.txt", "application/octet-stream", []byte("x"))
	if code != http.StatusConflict || !strings.Contains(text, quotaCopy) {
		t.Fatalf("full %d %s", code, text)
	}
	other, _ := h.account("other-quota@example.com", "Other", "correct-horse-1")
	sec, err := h.st.SystemSection(context.Background(), other.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, _, err := h.st.CreateAgent(context.Background(), other.ID, store.AgentInput{SectionID: sec.ID, Name: "Other", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	fileMachineFor(t, h, other, otherAgent, "running")
	sess, err := h.st.CreateSession(context.Background(), other.ID, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/agents/"+otherAgent.ID+"/uploads", strings.NewReader("ok"))
	req.RemoteAddr = ip
	req.ContentLength = 2
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-File-Name", "ok.txt")
	req.Header.Set("X-CSRF-Token", sess.CSRF)
	req.AddCookie(&http.Cookie{Name: cookieSession, Value: seal("session", sess.ID, h.srv.key)})
	code, text, _ = h.serve(req)
	if code != 200 || !strings.Contains(text, `"status"`) {
		t.Fatalf("other workspace %d %s", code, text)
	}
}

func TestFreeDiskRefusesUpload(t *testing.T) {
	ip := "203.0.113.153:9"
	h, _, agent := fileMachine(t, ip, "running")
	h.srv.SetDiskFree(func(string) (uint64, error) { return 1 << 30, nil })
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=files", "", ip, nil)
	if code != 200 || !strings.Contains(body, lowSpaceLead) || !strings.Contains(body, lowSpaceRest) || !strings.Contains(body, `id="upload-btn" disabled`) || !strings.Contains(body, `aria-describedby="low-space-panel"`) {
		t.Fatalf("panel %d %s", code, snippet(body))
	}
	code, text, _ := postFile(t, h, ip, agent.ID, "notes.txt", "application/octet-stream", []byte("nope"))
	if code != http.StatusConflict || strings.TrimSpace(text) != lowSpaceCopy {
		t.Fatalf("upload %d %q", code, text)
	}
	rows, err := h.st.ListUploads(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("stored %#v", rows)
	}
	code, chat, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || !strings.Contains(chat, `id="attach"`) || strings.Contains(chat, `id="attach" disabled`) {
		t.Fatal("attach starts disabled with the panel closed")
	}
}

func TestPreviewIsPlainText(t *testing.T) {
	ip := "203.0.113.154:9"
	h, _, agent := fileMachine(t, ip, "running")
	htmlBody := []byte("<html><script>alert(1)</script></html>")
	code, text, _ := postFile(t, h, ip, agent.ID, "page.html", "application/octet-stream", htmlBody)
	if code != 200 {
		t.Fatal(text)
	}
	id := jsonID(t, text)
	code, body, hdr := h.do(http.MethodGet, "/agents/"+agent.ID+"/files/"+id+"/preview", "", ip, nil)
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/plain") || strings.Contains(hdr.Get("Content-Type"), "text/html") {
		t.Fatalf("html %d %s", code, hdr.Get("Content-Type"))
	}
	if body != string(htmlBody) || hdr.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(hdr.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("preview headers %#v", hdr)
	}
	png := []byte{0x89, 'P', 'N', 'G'}
	code, text, _ = postFile(t, h, ip, agent.ID, "pic.png", "application/octet-stream", png)
	if code != 200 {
		t.Fatal(text)
	}
	code, body, hdr = h.do(http.MethodGet, "/agents/"+agent.ID+"/files/"+jsonID(t, text)+"/preview", "", ip, nil)
	if code != http.StatusUnsupportedMediaType || !strings.Contains(body, previewNote) || bytes.Contains([]byte(body), png) {
		t.Fatalf("png %d %s %s", code, hdr.Get("Content-Type"), body)
	}
	big := bytes.Repeat([]byte("a"), previewCap+1)
	code, text, _ = postFile(t, h, ip, agent.ID, "long.txt", "application/octet-stream", big)
	if code != 200 {
		t.Fatal(text)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/files/"+jsonID(t, text)+"/preview", "", ip, nil)
	if code != http.StatusUnsupportedMediaType || strings.Contains(body, strings.Repeat("a", 64)) {
		t.Fatalf("over cap %d %d", code, len(body))
	}
	okBody := bytes.Repeat([]byte("z"), previewCap)
	code, text, _ = postFile(t, h, ip, agent.ID, "ok.txt", "application/octet-stream", okBody)
	if code != 200 {
		t.Fatal(text)
	}
	code, body, hdr = h.do(http.MethodGet, "/agents/"+agent.ID+"/files/"+jsonID(t, text)+"/preview", "", ip, nil)
	if code != 200 || body != string(okBody) || !strings.HasPrefix(hdr.Get("Content-Type"), "text/plain") {
		t.Fatalf("256KB %d %d %s", code, len(body), hdr.Get("Content-Type"))
	}
}

func TestSearchFilesAcrossAgents(t *testing.T) {
	ip := "203.0.113.155:9"
	h, _, agent := fileMachine(t, ip, "paused")
	if err := h.st.ObserveWorkspace(context.Background(), agent.UserID, agent.ID, []store.ListedFile{{Name: "docs/pricing.md", Size: 20}}); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), agent.UserID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(context.Background(), agent.UserID, store.AgentInput{SectionID: sec.ID, Name: "Ledger", Title: "Books"}); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/search?q=pricing", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Files, all your agents") || !strings.Contains(body, "pricing.md") || !strings.Contains(body, "Paused agents show their last known files.") {
		t.Fatalf("search %d %s", code, snippet(body))
	}
	code, home, _ := h.do(http.MethodGet, "/?q=pricing", "", ip, nil)
	if code != 200 || !strings.Contains(home, "pricing.md") {
		t.Fatalf("home search %d", code)
	}
	other, secret := h.account("search-b@example.com", "Bee", "correct-horse-1")
	_ = other
	h.login("search-b@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ = h.do(http.MethodGet, "/search?q=pricing", "", ip, nil)
	if code != 200 || strings.Contains(body, "pricing.md") {
		t.Fatalf("cross user %d %s", code, snippet(body))
	}
}

func TestPathLineInNextMessage(t *testing.T) {
	ip := "203.0.113.156:9"
	h, _, agent := fileMachine(t, ip, "running")
	if _, err := h.st.CreateThread(context.Background(), agent.UserID, "dm", agent.ID, "Chat"); err != nil {
		t.Fatal(err)
	}
	code, text, _ := postFile(t, h, ip, agent.ID, "tiers-2026.csv", "application/octet-stream", []byte("a,b"))
	if code != 200 {
		t.Fatal(text)
	}
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"Please use the file"}}), ip, map[string]string{"HX-Request": "true"})
	want := "Atlas got the file at workspace/uploads/tiers-2026.csv, named in your message."
	if code != 200 || !strings.Contains(body, want) {
		t.Fatalf("message %d %s", code, snippet(body))
	}
	threads, err := h.st.ListThreadsByAgent(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	msgs, err := h.st.ListMessages(context.Background(), agent.UserID, threads[0].ID)
	if err != nil || len(msgs) == 0 || !strings.Contains(msgs[0].Body, want) {
		t.Fatalf("stored %#v %v", msgs, err)
	}
}

func TestReplaceLockHoldsUpload(t *testing.T) {
	ip := "203.0.113.157:9"
	h, fake, agent := fileMachine(t, ip, "running")
	repl, _, err := h.st.BeginReplacement(context.Background(), agent.UserID, agent.ID, "web:test", "example.invalid/castbot-runtime:s10", false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := fake.Snapshot()
	code, text, _ := postFile(t, h, ip, agent.ID, "notes.txt", "application/octet-stream", []byte("later"))
	after, _ := fake.Snapshot()
	if code != 200 || !strings.Contains(text, `"status":"held"`) || guestCall(before, after) {
		t.Fatalf("during replace %d %s calls %#v", code, text, after)
	}
	if err := h.st.FinishReplacement(context.Background(), agent.UserID, repl.ID, false); err != nil {
		t.Fatal(err)
	}
	const next = "https://new.example"
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "running", next, ""); err != nil {
		t.Fatal(err)
	}
	fake.SeedDisk(next, nil, map[string][]byte{})
	h.srv.fireAfterRun(context.Background(), agent.UserID, agent.ID)
	if bytes.Equal(fake.Disks["https://vm.example"].Files["uploads/notes.txt"], []byte("later")) {
		t.Fatal("wrote the old machine")
	}
	if !bytes.Equal(fake.Disks[next].Files["uploads/notes.txt"], []byte("later")) {
		t.Fatal("missing on the new machine")
	}
	if _, _, err := h.st.BeginReplacement(context.Background(), agent.UserID, agent.ID, "web:test", "example.invalid/castbot-runtime:s10b", false); err != nil {
		t.Fatal(err)
	}
	if err := h.st.ObserveWorkspace(context.Background(), agent.UserID, agent.ID, []store.ListedFile{{Name: "docs/pricing.md", Size: 4}}); err != nil {
		t.Fatal(err)
	}
	cached, err := h.st.ListCached(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(cached) != 1 {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/files/"+cached[0].ID+"/download", "", ip, nil)
	if code != http.StatusConflict || !strings.Contains(body, "This machine is being replaced.") {
		t.Fatalf("download %d %s", code, body)
	}
}

func TestFileAuthzAndCSRF(t *testing.T) {
	ip := "203.0.113.158:9"
	h, _, agent := fileMachine(t, ip, "running")
	code, text, _ := postFile(t, h, ip, agent.ID, "notes.txt", "application/octet-stream", []byte("secret-file-body"))
	if code != 200 {
		t.Fatal(text)
	}
	id := jsonID(t, text)
	req := h.rawReq(http.MethodPost, "/agents/"+agent.ID+"/uploads", strings.NewReader("x"), ip)
	req.ContentLength = 1
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-File-Name", "nope.txt")
	code, _, _ = h.serve(req)
	if code != http.StatusForbidden {
		t.Fatalf("csrf %d", code)
	}
	other, secret := h.account("authz-b@example.com", "Bee", "correct-horse-1")
	_ = other
	h.login("authz-b@example.com", "correct-horse-1", secret, ip, false)
	for _, path := range []string{
		"/agents/" + agent.ID + "?panel=files",
		"/agents/" + agent.ID + "/files",
		"/agents/" + agent.ID + "/files/" + id + "/download",
		"/agents/" + agent.ID + "/files/" + id + "/preview",
	} {
		code, body, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusNotFound || strings.Contains(body, "secret-file-body") {
			t.Fatalf("%s -> %d", path, code)
		}
	}
	code, body, _ := h.do(http.MethodGet, "/search?q=notes.txt", "", ip, nil)
	if code != 200 || strings.Contains(body, "notes.txt") {
		t.Fatalf("search %d %s", code, snippet(body))
	}
}

func fileMachine(t *testing.T, ip, state string) (*harness, *host.Fake, store.Agent) {
	t.Helper()
	h, fake, agent := machineUser(t, ip)
	h.srv.SetDataDir(t.TempDir())
	fileMachineFor(t, h, store.User{ID: agent.UserID}, agent, state)
	return h, fake, agent
}

func fileMachineFor(t *testing.T, h *harness, user store.User, agent store.Agent, state string) {
	t.Helper()
	if user.ID == "" {
		user.ID = agent.UserID
	}
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s10"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-files", "https://vm.example", state, ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	fake := h.hostFake(t)
	fake.Machines["vm-files"] = host.Machine{ID: "vm-files", State: state, Endpoint: "https://vm.example"}
	if fake.Disks == nil || fake.Disks["https://vm.example"] == nil {
		fake.SeedDisk("https://vm.example", nil, map[string][]byte{})
	}
}

func (h *harness) hostFake(t *testing.T) *host.Fake {
	t.Helper()
	h.srv.mu.Lock()
	defer h.srv.mu.Unlock()
	fake, ok := h.srv.machine.(*host.Fake)
	if !ok {
		t.Fatal("host is not the fake")
	}
	return fake
}

func postFile(t *testing.T, h *harness, ip, agentID, name, contentType string, body []byte) (int, string, http.Header) {
	t.Helper()
	req := h.rawReq(http.MethodPost, "/agents/"+agentID+"/uploads", bytes.NewReader(body), ip)
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-File-Name", name)
	req.Header.Set("X-CSRF-Token", h.csrf)
	return h.serve(req)
}

func (h *harness) rawReq(method, path string, body io.Reader, ip string) *http.Request {
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = ip
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.jar {
		req.AddCookie(c)
	}
	return req
}

func (h *harness) serve(req *http.Request) (int, string, http.Header) {
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw), res.Header
}

type chunkWriter struct {
	hdr    http.Header
	code   int
	writes int
	max    int
	buf    bytes.Buffer
}

func (c *chunkWriter) Header() http.Header {
	if c.hdr == nil {
		c.hdr = http.Header{}
	}
	return c.hdr
}

func (c *chunkWriter) WriteHeader(code int) { c.code = code }

func (c *chunkWriter) Write(p []byte) (int, error) {
	if c.code == 0 {
		c.code = 200
	}
	c.writes++
	if len(p) > c.max {
		c.max = len(p)
	}
	return c.buf.Write(p)
}

func getRaw(t *testing.T, h *harness, ip, path string) *chunkWriter {
	t.Helper()
	req := h.rawReq(http.MethodGet, path, nil, ip)
	w := &chunkWriter{}
	h.srv.Handler().ServeHTTP(w, req)
	return w
}

type errAfter struct {
	left int
}

func (e *errAfter) Read(p []byte) (int, error) {
	if e.left <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := len(p)
	if n > e.left {
		n = e.left
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	e.left -= n
	return n, nil
}

func guestCall(before, after []string) bool {
	for _, call := range after[len(before):] {
		switch call {
		case "list-files", "read-file", "write-file", "wake", "probe":
			return true
		}
	}
	return false
}

func addedCall(before, after []string, want string) bool {
	for _, call := range after[len(before):] {
		if call == want {
			return true
		}
	}
	return false
}

func eventKind(events []store.VMEvent, kind string) bool {
	for _, ev := range events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func jsonID(t *testing.T, text string) string {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal([]byte(text), &got); err != nil || got["id"] == "" {
		t.Fatalf("id %s %v", text, err)
	}
	return got["id"]
}

func waitLog(t *testing.T, h *harness, needle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(h.srv.UploadLog(), "\n"), needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log never showed %s (%#v)", needle, h.srv.UploadLog())
}

type errString string

func (e errString) Error() string { return string(e) }

func TestUploadNameKeepsCounting(t *testing.T) {
	taken := map[string]struct{}{"report.csv": {}}
	for n := 2; n < 100; n++ {
		taken["report ("+strconv.Itoa(n)+").csv"] = struct{}{}
	}
	got, ok := nextUploadName(taken, "report.csv")
	if !ok || got != "report (100).csv" {
		t.Fatalf("got %q ok %v", got, ok)
	}
	for n := 100; n < 10000; n++ {
		taken["report ("+strconv.Itoa(n)+").csv"] = struct{}{}
	}
	if _, ok := nextUploadName(taken, "report.csv"); ok {
		t.Fatal("reused a name")
	}

	ip := "203.0.113.160:9"
	h, fake, agent := fileMachine(t, ip, "running")
	ctx := context.Background()
	for n := 2; n < 100; n++ {
		name := "report.csv"
		if n > 0 {
			name = "report (" + strconv.Itoa(n) + ").csv"
		}
		if _, err := h.st.InsertUpload(ctx, agent.UserID, agent.ID, "", name, "", "aa", "workspace/uploads/"+name, "copied", 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.st.InsertUpload(ctx, agent.UserID, agent.ID, "", "report.csv", "", "aa", "workspace/uploads/report.csv", "copied", 1); err != nil {
		t.Fatal(err)
	}
	code, text, _ := postFile(t, h, ip, agent.ID, "report.csv", "application/octet-stream", []byte("new"))
	if code != 200 || !strings.Contains(text, `"name":"report (100).csv"`) {
		t.Fatalf("upload %d %s", code, text)
	}
	if _, ok := fake.Disks["https://vm.example"].Files["uploads/report.csv"]; ok {
		t.Fatal("wrote the original name")
	}
	if string(fake.Disks["https://vm.example"].Files["uploads/report (100).csv"]) != "new" {
		t.Fatal("missing numbered file")
	}
}

func TestDiskLowFailsClosed(t *testing.T) {
	ip := "203.0.113.161:9"
	h, _, agent := fileMachine(t, ip, "running")
	h.srv.SetDiskFree(func(string) (uint64, error) { return 0, errString("statfs") })
	code, text, _ := postFile(t, h, ip, agent.ID, "notes.txt", "application/octet-stream", []byte("x"))
	if code != http.StatusConflict || strings.TrimSpace(text) != lowSpaceCopy {
		t.Fatalf("statfs %d %s", code, text)
	}
	h.srv.SetDataDir("")
	h.srv.SetDiskFree(nil)
	code, text, _ = postFile(t, h, ip, agent.ID, "notes.txt", "application/octet-stream", []byte("x"))
	if code != http.StatusConflict || strings.TrimSpace(text) != lowSpaceCopy {
		t.Fatalf("empty dir %d %s", code, text)
	}
}

func TestEscapePaths(t *testing.T) {
	ip := "203.0.113.162:9"
	h, fake, agent := fileMachine(t, ip, "running")
	const secret = "ESCAPED-SECRET"
	fake.SeedDisk("https://vm.example", nil, map[string][]byte{
		"../etc/passwd":        []byte(secret),
		"/etc/passwd":          []byte(secret),
		"docs/ok.md":           []byte("hello"),
		"..%2f..%2fetc/passwd": []byte(secret),
		"notes\x00.txt":        []byte(secret),
		"../secret.md":         []byte(secret),
	})
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=files", "", ip, nil)
	if code != 200 || strings.Contains(page, secret) || strings.Contains(page, "../") || !strings.Contains(page, "ok.md") {
		t.Fatalf("list %d secret %v", code, strings.Contains(page, secret))
	}
	cached, err := h.st.ListCached(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range cached {
		if !safeGuestPath(row.Name) || strings.Contains(row.Name, "..") {
			t.Fatalf("cached %q", row.Name)
		}
	}

	names := []string{
		"../secret.txt",
		"/etc/passwd",
		"a/../../outside.txt",
		"notes\x00.txt",
		"..%2F..%2Fetc/passwd",
		"..%2f..%2fetc%2fpasswd",
		"docs%2Fpricing.md",
	}
	for _, name := range names {
		code, text, _ := postFile(t, h, ip, agent.ID, name, "application/octet-stream", []byte("nope"))
		if strings.Contains(text, secret) {
			t.Fatalf("%q leaked", name)
		}
		if code == 200 && (strings.Contains(text, "..") || strings.Contains(text, "/etc/") || strings.Contains(text, "%2f") || strings.Contains(text, "%2F")) {
			t.Fatalf("%q accepted as %s", name, text)
		}
	}
	for key, body := range fake.Disks["https://vm.example"].Files {
		if string(body) != "nope" {
			continue
		}
		if !strings.HasPrefix(key, "uploads/") || strings.Contains(key, "..") || strings.Contains(key, "%2") {
			t.Fatalf("wrote %q", key)
		}
	}

	id, err := h.st.PlantCached(context.Background(), agent.UserID, agent.ID, "../secret.md", int64(len(secret)))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := fake.Snapshot()
	res := getRaw(t, h, ip, "/agents/"+agent.ID+"/files/"+id+"/download")
	if res.code == 200 || strings.Contains(res.buf.String(), secret) {
		t.Fatalf("download %d %q", res.code, res.buf.String())
	}
	prev := getRaw(t, h, ip, "/agents/"+agent.ID+"/files/"+id+"/preview")
	if prev.code == 200 || strings.Contains(prev.buf.String(), secret) {
		t.Fatalf("preview %d %q", prev.code, prev.buf.String())
	}
	after, _ := fake.Snapshot()
	if addedCall(before, after, "read-file") || addedCall(before, after, "list-files") {
		t.Fatalf("guest read %#v", after[len(before):])
	}
}

func TestAttachmentKeepsRealName(t *testing.T) {
	got := attachmentDisposition("report (2).csv")
	if !strings.Contains(got, `filename="report2.csv"`) || !strings.Contains(got, "filename*=UTF-8''report%20%282%29.csv") {
		t.Fatal(got)
	}
	ip := "203.0.113.163:9"
	h, _, agent := fileMachine(t, ip, "running")
	body := []byte("csv")
	code, text, _ := postFile(t, h, ip, agent.ID, "report.csv", "application/octet-stream", body)
	if code != 200 {
		t.Fatal(text)
	}
	code, text, _ = postFile(t, h, ip, agent.ID, "report.csv", "application/octet-stream", body)
	if code != 200 || !strings.Contains(text, "report (2).csv") {
		t.Fatalf("%d %s", code, text)
	}
	var row map[string]string
	if err := json.Unmarshal([]byte(text), &row); err != nil {
		t.Fatal(err)
	}
	res := getRaw(t, h, ip, "/agents/"+agent.ID+"/files/"+row["id"]+"/download")
	disp := res.hdr.Get("Content-Disposition")
	if res.code != 200 || !strings.Contains(disp, `filename="report2.csv"`) || !strings.Contains(disp, "filename*=UTF-8''report%20%282%29.csv") {
		t.Fatalf("%d %s", res.code, disp)
	}
}

func TestAgentReplyDownloadLink(t *testing.T) {
	ip := "203.0.113.164:9"
	h, fake, agent := fileMachine(t, ip, "running")
	fake.SeedDisk("https://vm.example", nil, map[string][]byte{"docs/pricing.md": bytes.Repeat([]byte("p"), 3*1024)})
	if err := h.st.ObserveWorkspace(context.Background(), agent.UserID, agent.ID, []store.ListedFile{{Name: "docs/pricing.md", Size: 3 * 1024}}); err != nil {
		t.Fatal(err)
	}
	threads, err := h.st.ListThreadsByAgent(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var threadID string
	for _, th := range threads {
		if th.Kind != "cli" {
			threadID = th.ID
			break
		}
	}
	if threadID == "" {
		t.Fatal("no chat thread")
	}
	if _, err := h.st.CreateMessage(context.Background(), agent.UserID, threadID, "agent", agent.ID, "Done. The table is in docs/pricing.md."); err != nil {
		t.Fatal(err)
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || !strings.Contains(page, `aria-label="Download pricing.md"`) || !strings.Contains(page, "workspace/docs · 3 KB · changed") {
		t.Fatalf("link %d", code)
	}
}
