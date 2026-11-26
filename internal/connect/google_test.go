package connect

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGoogleCatalog(t *testing.T) {
	entry, ok := Lookup(Google)
	if !ok || entry.SecretName != SecretNameGoogle || entry.Auth != AuthBasicPair || entry.ExtraLabel != "Gmail address" || entry.TokenLabel != "App password" {
		t.Fatalf("%+v %v", entry, ok)
	}
	if len(entry.Hosts) != 3 || entry.Hosts[0] != googleIMAP || entry.Hosts[1] != googleSMTP || entry.Hosts[2] != googleCal || entry.APIHost != googleCal {
		t.Fatal(entry.Hosts)
	}
	if !BlocksGuest(SecretNameGoogle) || !BlocksGuest("GMAIL_APP_PASSWORD") || !BlocksGuest("GOOGLE_APP_PASSWORD") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("GMAIL_APP_PASSWORD"); !ok || id != Google {
		t.Fatal("alias")
	}
	writes := map[string]bool{}
	for _, tool := range Tools(Google) {
		writes[tool.Name] = tool.Write
		if tool.Run == nil {
			t.Fatal(tool.Name)
		}
	}
	if writes["google_list_messages"] || writes["google_get_message"] || writes["google_list_events"] {
		t.Fatal("read marked write")
	}
	if !writes["google_create_draft"] || !writes["google_send"] || !writes["google_create_event"] || len(writes) != 6 {
		t.Fatal(writes)
	}
	yml, ok := ShimYAML(Google)
	if !ok || strings.Contains(yml, "GMAIL") || strings.Contains(yml, "GOOGLE_CONNECTION") || !strings.Contains(yml, "\n  - google\n") {
		t.Fatal(yml)
	}
	email, secret, err := googleCreds(WithVault(context.Background(), "abcd efgh", "ada@gmail.com"))
	if err != nil || email != "ada@gmail.com" || secret != "abcdefgh" {
		t.Fatalf("%s %s %v", email, secret, err)
	}
	if _, _, err := googleCreds(WithVault(context.Background(), "secret", "ada")); err == nil {
		t.Fatal("bad email")
	}
	if _, _, err := googleCreds(WithVault(context.Background(), "bad\"pass", "ada@gmail.com")); err == nil {
		t.Fatal("bad password")
	}
}

func TestGoogleBodyStructure(t *testing.T) {
	parts, err := googleBodyParts(`("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 11 1)`)
	if err != nil || len(parts) != 1 || parts[0].section != "1" || parts[0].media != "TEXT" || parts[0].subtype != "PLAIN" || parts[0].size != 11 {
		t.Fatalf("%+v %v", parts, err)
	}
	if googlePickText(parts).section != "1" {
		t.Fatal(parts)
	}
	nested := `((("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 5 1)("TEXT" "HTML" ("CHARSET" "UTF-8") NIL NIL "7BIT" 20 1) "ALTERNATIVE")("APPLICATION" "PDF" ("NAME" "note.pdf") NIL NIL "BASE64" 400) "MIXED")`
	parts, err = googleBodyParts(nested)
	if err != nil || len(parts) != 3 {
		t.Fatalf("%+v %v", parts, err)
	}
	text := googlePickText(parts)
	if text.section != "1.1" || text.subtype != "PLAIN" {
		t.Fatalf("%+v", parts)
	}
	atts := googleAttachments(parts, text.section)
	if len(atts) != 1 || atts[0].Filename != "note.pdf" || atts[0].Size != 400 || atts[0].Type != "APPLICATION/PDF" {
		t.Fatalf("%+v", atts)
	}
	for _, part := range parts {
		if part.media == "APPLICATION" && part.section != "2" {
			t.Fatal(part.section)
		}
	}
	if _, err := googleBodyParts("("); err == nil {
		t.Fatal("broken structure")
	}
	raw := "BEGIN:VEVENT\r\nSUMMARY:Hello\r\n there\r\nDTSTART;TZID=America/Los_Angeles:20260101T090000\r\nDTEND:20260101T100000Z\r\nUID:abc@castbot\r\nEND:VEVENT\r\n"
	events := googleEvents(raw)
	if len(events) != 1 || events[0].Summary != "Hellothere" || events[0].Start != "20260101T090000" || events[0].UID != "abc@castbot" {
		t.Fatal(events)
	}
	ics := googleICS("abc@castbot", "Meet, soon", "a;b", time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC), time.Date(2026, 1, 2, 16, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(ics, `SUMMARY:Meet\, soon`) || !strings.Contains(ics, `DESCRIPTION:a\;b`) || strings.Contains(ics, "Meet, soon") {
		t.Fatal(ics)
	}
}

func TestGoogleWriteDoesNotReachUpstream(t *testing.T) {
	var hits int
	prev := googleDial
	googleDial = func(context.Context, string, string) (net.Conn, error) {
		hits++
		return nil, ErrUpstream
	}
	t.Cleanup(func() { googleDial = prev })
	ctx := WithVault(context.Background(), "apppasswordvalue", "ada@gmail.com")
	args := map[string]any{"to": "bob@example.com", "subject": "Hello", "body": "See you", "title": "Meet", "start": "2026-01-02T15:00:00Z", "end": "2026-01-02T16:00:00Z"}
	for _, name := range []string{"google_send", "google_create_draft", "google_create_event"} {
		_, err := HandleRPC(ctx, Google, rpcCall(name, args), false, nil)
		if !errors.Is(err, ErrDenied) || hits != 0 {
			t.Fatalf("%s %v hits %d", name, err, hits)
		}
	}
	out, err := HandleRPC(ctx, Google, rpcCall("google_delete", args), true, nil)
	if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
	if _, err := googleTCP(ctx, "evil.example", "993"); !errors.Is(err, ErrAllow) || hits != 0 {
		t.Fatalf("evil %v hits %d", err, hits)
	}
	if _, err := googleTCP(ctx, googleIMAP, "143"); !errors.Is(err, ErrAllow) || hits != 0 {
		t.Fatalf("port %v hits %d", err, hits)
	}
	if _, err := googleListMessages(WithVault(ctx, "apppasswordvalue", "not-an-email"), nil); !errors.Is(err, ErrBad) || hits != 0 {
		t.Fatalf("email %v", err)
	}
	if _, err := googleGetMessage(ctx, map[string]any{"uid": "12a"}); !errors.Is(err, ErrBad) || hits != 0 {
		t.Fatalf("uid %v", err)
	}
	if _, err := googleSend(ctx, map[string]any{"to": "a@b.com, c@d.com", "subject": "S", "body": "B"}); !errors.Is(err, ErrBad) || hits != 0 {
		t.Fatalf("recipients %v", err)
	}
	if _, err := googleListEvents(ctx, map[string]any{"start": "2026-01-01T00:00:00Z"}); !errors.Is(err, ErrBad) || hits != 0 {
		t.Fatalf("range %v", err)
	}
}

func TestGoogleListMessages(t *testing.T) {
	const owner = "ownerapppassword01"
	const other = "otherapppassword02"
	tape := listenGoogle(t, googleIMAP, "993", func(conn net.Conn, tape *googleTape) {
		serveIMAP(conn, tape, func(line string) string {
			switch {
			case strings.Contains(line, " EXAMINE "):
				if !strings.Contains(line, " EXAMINE INBOX") {
					t.Errorf("mailbox %s", line)
				}
				return imapOK(line, "* 2 EXISTS\r\n")
			case strings.Contains(line, " UID SEARCH "):
				return imapOK(line, "* SEARCH 10 11\r\n")
			case strings.Contains(line, " UID FETCH "):
				h1 := "From: a@b.com\r\nSubject: Hello\r\nDate: Mon, 1 Jan 2026 00:00:00 +0000\r\n\r\n"
				h2 := "From: c@d.com\r\nSubject: " + other + "\r\nDate: Tue, 2 Jan 2026 00:00:00 +0000\r\n\r\n"
				var b strings.Builder
				fmt.Fprintf(&b, "* 1 FETCH (UID 10 BODY[HEADER.FIELDS (FROM SUBJECT DATE)] {%d}\r\n%s)\r\n", len(h1), h1)
				fmt.Fprintf(&b, "* 2 FETCH (UID 11 BODY[HEADER.FIELDS (FROM SUBJECT DATE)] {%d}\r\n%s)\r\n", len(h2), h2)
				return imapOK(line, b.String())
			default:
				return imapOK(line, "")
			}
		})
	})
	ctx, cancel := googleTestCtx(other, "ada@gmail.com")
	defer cancel()
	out, err := HandleRPC(ctx, Google, rpcCall("google_list_messages", map[string]any{"limit": "50", "mailbox": "[Gmail]/All Mail"}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := rpcText(t, out)
	if strings.Contains(text, other) || strings.Contains(text, owner) || !strings.Contains(text, "[redacted]") || !strings.Contains(text, "Hello") {
		t.Fatal(text)
	}
	var parsed struct {
		Messages []googleListed `json:"messages"`
	}
	if json.Unmarshal([]byte(text), &parsed) != nil || len(parsed.Messages) != 2 || parsed.Messages[0].UID != "10" || parsed.Messages[1].Subject != "[redacted]" {
		t.Fatal(text)
	}
	raw := tape.text()
	if !strings.Contains(raw, `LOGIN "ada@gmail.com" "`+other+`"`) || strings.Contains(raw, owner) || strings.Contains(raw, "All Mail") || !strings.Contains(raw, "EXAMINE INBOX") {
		t.Fatal(raw)
	}
	assertNoMutation(t, raw)
	if dialed := tape.dialed(); len(dialed) != 1 || dialed[0] != googleIMAP+":993" {
		t.Fatal(dialed)
	}
}

func TestGoogleListCapsTheCount(t *testing.T) {
	tape := listenGoogle(t, googleIMAP, "993", func(conn net.Conn, tape *googleTape) {
		serveIMAP(conn, tape, func(line string) string {
			if strings.Contains(line, " UID SEARCH ") {
				ids := make([]string, 25)
				for i := range ids {
					ids[i] = fmt.Sprintf("%d", i+1)
				}
				return imapOK(line, "* SEARCH "+strings.Join(ids, " ")+"\r\n")
			}
			return imapOK(line, "")
		})
	})
	ctx, cancel := googleTestCtx("apppasswordvalue", "ada@gmail.com")
	defer cancel()
	if _, err := googleListMessages(ctx, map[string]any{"limit": "50"}); err != nil {
		t.Fatal(err)
	}
	raw := tape.text()
	if !strings.Contains(raw, "UID FETCH 6,7,8,") || !strings.Contains(raw, ",25 ") || strings.Contains(raw, "FETCH 1,") {
		t.Fatal(raw)
	}
}

func TestGoogleGetMessage(t *testing.T) {
	const secret = "apppasswordvalue"
	header := "From: a@b.com\r\nTo: ada@gmail.com\r\nSubject: Hello\r\nDate: Mon, 1 Jan 2026 00:00:00 +0000\r\n\r\n"
	structure := `((("TEXT" "PLAIN" ("CHARSET" "UTF-8") NIL NIL "7BIT" 5 1)("TEXT" "HTML" ("CHARSET" "UTF-8") NIL NIL "7BIT" 20 1) "ALTERNATIVE")("APPLICATION" "PDF" ("NAME" "note.pdf") NIL NIL "BASE64" 400) "MIXED")`
	tape := listenGoogle(t, googleIMAP, "993", func(conn net.Conn, tape *googleTape) {
		serveIMAP(conn, tape, func(line string) string {
			switch {
			case strings.Contains(line, "BODYSTRUCTURE"):
				fetch := fmt.Sprintf("* 1 FETCH (UID 5 BODYSTRUCTURE %s BODY[HEADER] {%d}\r\n%s)\r\n", structure, len(header), header)
				return imapOK(line, fetch)
			case strings.Contains(line, "BODY.PEEK["):
				if !strings.Contains(line, "BODY.PEEK[1.1]<0.65536>") {
					return imapOK(line, "* 1 FETCH (UID 5 BODY[2]<0> {4}\r\nPDFx)\r\n")
				}
				return imapOK(line, "* 1 FETCH (UID 5 BODY[1.1]<0> {5}\r\nHello)\r\n")
			default:
				return imapOK(line, "")
			}
		})
	})
	ctx, cancel := googleTestCtx(secret, "ada@gmail.com")
	defer cancel()
	out, err := HandleRPC(ctx, Google, rpcCall("google_get_message", map[string]any{"uid": "5", "mailbox": "Sent"}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := rpcText(t, out)
	if strings.Contains(text, secret) || strings.Contains(text, "PDFx") {
		t.Fatal(text)
	}
	var got googleGot
	if json.Unmarshal([]byte(text), &got) != nil || got.Text != "Hello" || got.From != "a@b.com" || got.Subject != "Hello" || len(got.Attachments) != 1 || got.Attachments[0].Filename != "note.pdf" || got.Attachments[0].Size != 400 {
		t.Fatalf("%+v %s", got, text)
	}
	raw := tape.text()
	if !strings.Contains(raw, "BODY.PEEK[1.1]<0.65536>") || strings.Contains(raw, "BODY.PEEK[2]") || strings.Contains(raw, "EXAMINE Sent") || !strings.Contains(raw, "EXAMINE INBOX") {
		t.Fatal(raw)
	}
	assertNoMutation(t, raw)
}

func TestGoogleCreateDraft(t *testing.T) {
	const secret = "apppasswordvalue"
	tape := listenGoogle(t, googleIMAP, "993", func(conn net.Conn, tape *googleTape) {
		serveIMAP(conn, tape, func(line string) string { return imapOK(line, "") })
	})
	ctx, cancel := googleTestCtx(secret, "ada@gmail.com")
	defer cancel()
	out, err := HandleRPC(ctx, Google, rpcCall("google_create_draft", map[string]any{
		"to": "bob@example.com", "subject": "Hello", "body": "See you",
	}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := rpcText(t, out)
	if strings.Contains(text, secret) || !strings.Contains(text, `"draft":true`) {
		t.Fatal(text)
	}
	raw := tape.text()
	sent := raw
	if i := strings.Index(raw, "APPEND"); i >= 0 {
		sent = raw[i:]
	}
	if !strings.Contains(raw, `APPEND "[Gmail]/Drafts"`) || !strings.Contains(sent, "To: bob@example.com") || !strings.Contains(sent, "Subject: Hello") || !strings.Contains(sent, "See you") || strings.Contains(sent, secret) || !strings.Contains(raw, `LOGIN "ada@gmail.com" "`+secret+`"`) {
		t.Fatal(raw)
	}
	assertNoMutation(t, raw)
}

func TestGoogleSend(t *testing.T) {
	const secret = "apppasswordvalue"
	const email = "ada@gmail.com"
	tape := listenGoogle(t, googleSMTP, "465", func(conn net.Conn, tape *googleTape) {
		serveSMTP(conn, tape)
	})
	ctx, cancel := googleTestCtx(secret, email)
	defer cancel()
	out, err := HandleRPC(ctx, Google, rpcCall("google_send", map[string]any{
		"to": "bob@example.com", "subject": "Hello", "body": "See you",
	}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := rpcText(t, out)
	if strings.Contains(text, secret) || !strings.Contains(text, `"sent":true`) {
		t.Fatal(text)
	}
	raw := tape.text()
	if strings.Contains(raw, secret) && !strings.Contains(raw, "AUTH PLAIN ") {
		t.Fatal(raw)
	}
	plain := ""
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "AUTH PLAIN ") {
			plain = strings.TrimSpace(strings.TrimPrefix(line, "AUTH PLAIN "))
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(plain)
	if err != nil || string(decoded) != "\x00"+email+"\x00"+secret {
		t.Fatalf("auth %q %v", plain, err)
	}
	if strings.Count(raw, "RCPT TO:") != 1 || !strings.Contains(raw, "RCPT TO:<bob@example.com>") || !strings.Contains(raw, "Subject: Hello") || !strings.Contains(raw, "To: bob@example.com") {
		t.Fatal(raw)
	}
	if dialed := tape.dialed(); len(dialed) != 1 || dialed[0] != googleSMTP+":465" {
		t.Fatal(dialed)
	}
}

func TestGoogleCalendar(t *testing.T) {
	const secret = "apppasswordvalue"
	const email = "ada@gmail.com"
	var mu sync.Mutex
	var putPath, putBody string
	hosts := googleCalServer(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != email || pass != secret {
			t.Errorf("auth %s %s", user, pass)
		}
		if r.Host != googleCal && !strings.HasPrefix(r.Host, googleCal+":") {
			t.Errorf("host %s", r.Host)
		}
		switch r.Method {
		case "REPORT":
			if r.Header.Get("Depth") != "1" {
				t.Errorf("depth %s", r.Header.Get("Depth"))
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "20260101T000000Z") || !strings.Contains(string(body), "20260102T000000Z") {
				t.Errorf("report %s", body)
			}
			fmt.Fprintf(w, "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:e@castbot\r\nSUMMARY:%s\r\nDTSTART:20260101T000000Z\r\nDTEND:20260102T000000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", secret)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			putPath = r.URL.Path
			putBody = string(body)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	ctx, cancel := googleTestCtx(secret, email)
	defer cancel()
	out, err := HandleRPC(ctx, Google, rpcCall("google_list_events", map[string]any{
		"start": "2026-01-01T00:00:00Z", "end": "2026-01-02T00:00:00Z",
	}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := rpcText(t, out)
	if strings.Contains(text, secret) || !strings.Contains(text, "[redacted]") || !strings.Contains(text, "e@castbot") {
		t.Fatal(text)
	}
	out, err = HandleRPC(ctx, Google, rpcCall("google_create_event", map[string]any{
		"title": "Meet, soon", "start": "2026-01-02T15:04:05Z", "end": "2026-01-02T16:00:00Z", "description": "a;b",
	}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text = rpcText(t, out)
	if strings.Contains(text, secret) || !strings.Contains(text, `"created":true`) {
		t.Fatal(text)
	}
	var created struct {
		UID string `json:"uid"`
	}
	if json.Unmarshal([]byte(text), &created) != nil || !strings.HasSuffix(created.UID, "@castbot") {
		t.Fatal(text)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(putPath, created.UID) || !strings.HasSuffix(putPath, ".ics") || !strings.Contains(putBody, `SUMMARY:Meet\, soon`) || !strings.Contains(putBody, `DESCRIPTION:a\;b`) || strings.Contains(putBody, secret) {
		t.Fatalf("path %s body %s", putPath, putBody)
	}
	if dialed := hosts(); len(dialed) != 2 {
		t.Fatal(dialed)
	}
	for _, host := range hosts() {
		if host != googleCal+":443" {
			t.Fatal(host)
		}
	}
}

func TestGoogleCalendarRedirectIsRefused(t *testing.T) {
	hosts := googleCalServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/x", http.StatusFound)
	})
	ctx, cancel := googleTestCtx("apppasswordvalue", "ada@gmail.com")
	defer cancel()
	_, err := googleHTTPS(ctx, http.MethodGet, "https://"+googleCal+"/caldav/v2/ada@gmail.com/events/", "", nil)
	if !errors.Is(err, ErrRedirect) {
		t.Fatal(err)
	}
	if dialed := hosts(); len(dialed) != 1 || dialed[0] != googleCal+":443" {
		t.Fatal(dialed)
	}
}

func TestGoogleBasicPair(t *testing.T) {
	const secret = "apppasswordvalue"
	const email = "ada@gmail.com"
	var mu sync.Mutex
	var auth, host string
	var hits int
	ts, dial := connectServer(t, []string{googleCal}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		auth = r.Header.Get("Authorization")
		host = r.Host
		mu.Unlock()
		_, _ = w.Write([]byte(secret))
	})
	defer ts.Close()
	ctx := WithVault(context.Background(), secret, email)
	res, err := Do(ctx, secret, Call{
		Connection: Google, Method: "GET", URL: "https://" + googleCal + "/caldav/v2/x/events/",
	}, dial)
	if err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+secret))
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 || auth != want || strings.Contains(auth, "Bearer") {
		t.Fatalf("hits %d auth %q", hits, auth)
	}
	if host != googleCal && !strings.HasPrefix(host, googleCal+":") {
		t.Fatal(host)
	}
	if strings.Contains(string(res.Body), secret) || !strings.Contains(string(res.Body), "[redacted]") {
		t.Fatal(string(res.Body))
	}
	res, err = Do(ctx, secret, Call{Connection: Google, Method: "GET", URL: "https://evil.example/caldav"}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 1 {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
}

type googleTape struct {
	mu    sync.Mutex
	b     strings.Builder
	hits  int
	hosts []string
}

func (g *googleTape) add(s string) {
	g.mu.Lock()
	g.b.WriteString(s)
	g.mu.Unlock()
}

func (g *googleTape) text() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.b.String()
}

func (g *googleTape) dialed() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.hosts...)
}

func listenGoogle(t *testing.T, host, port string, serve func(net.Conn, *googleTape)) *googleTape {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tape := &googleTape{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				tape.mu.Lock()
				tape.hits++
				tape.mu.Unlock()
				serve(c, tape)
				_ = c.Close()
			}()
		}
	}()
	prev := googleDial
	googleDial = func(ctx context.Context, h, p string) (net.Conn, error) {
		tape.mu.Lock()
		tape.hosts = append(tape.hosts, h+":"+p)
		tape.mu.Unlock()
		if h != host || p != port {
			return nil, ErrAllow
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ln.Addr().String())
	}
	t.Cleanup(func() {
		googleDial = prev
		_ = ln.Close()
	})
	return tape
}

func googleCalServer(t *testing.T, h http.HandlerFunc) func() []string {
	t.Helper()
	cert, pool := testCert(t, googleCal)
	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	var mu sync.Mutex
	var hosts []string
	prevDial, prevRoots := googleDial, googleRoots
	googleRoots = pool
	googleDial = func(ctx context.Context, host, port string) (net.Conn, error) {
		mu.Lock()
		hosts = append(hosts, host+":"+port)
		mu.Unlock()
		if host != googleCal || port != "443" {
			return nil, ErrAllow
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ts.Listener.Addr().String())
	}
	t.Cleanup(func() {
		googleDial = prevDial
		googleRoots = prevRoots
		ts.Close()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hosts...)
	}
}

func serveIMAP(conn net.Conn, tape *googleTape, reply func(line string) string) {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "* OK ready\r\n")
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		tape.add(line)
		if strings.Contains(line, " APPEND ") {
			n, ok := trailingLiteral(line)
			if !ok {
				return
			}
			if _, err := io.WriteString(conn, "+ go\r\n"); err != nil {
				return
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			tape.add(string(buf))
			tag := "a1"
			if fields := strings.Fields(line); len(fields) > 0 {
				tag = fields[0]
			}
			_, _ = io.WriteString(conn, tag+" OK\r\n")
			continue
		}
		if _, err := io.WriteString(conn, reply(line)); err != nil {
			return
		}
	}
}

func serveSMTP(conn net.Conn, tape *googleTape) {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "220 ready\r\n")
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		tape.add(line)
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			_, _ = io.WriteString(conn, "250-ready\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(upper, "AUTH "):
			_, _ = io.WriteString(conn, "235 ok\r\n")
		case strings.HasPrefix(upper, "DATA"):
			_, _ = io.WriteString(conn, "354 go\r\n")
			for {
				data, err := r.ReadString('\n')
				if err != nil {
					return
				}
				tape.add(data)
				if data == ".\r\n" {
					break
				}
			}
			_, _ = io.WriteString(conn, "250 ok\r\n")
		case strings.HasPrefix(upper, "QUIT"):
			_, _ = io.WriteString(conn, "221 bye\r\n")
			return
		default:
			_, _ = io.WriteString(conn, "250 ok\r\n")
		}
	}
}

func imapOK(line, untagged string) string {
	tag := "a1"
	if fields := strings.Fields(line); len(fields) > 0 {
		tag = fields[0]
	}
	return untagged + tag + " OK\r\n"
}

func googleTestCtx(secret, email string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	return WithVault(ctx, secret, email), cancel
}

func assertNoMutation(t *testing.T, raw string) {
	t.Helper()
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		verb := fields[0]
		if len(fields) > 1 && strings.HasPrefix(verb, "a") && googleDigits(strings.TrimPrefix(verb, "a")) {
			verb = fields[1]
		}
		switch strings.ToUpper(verb) {
		case "DELETE", "EXPUNGE", "STORE", "SETACL":
			t.Fatal(line)
		}
	}
}
