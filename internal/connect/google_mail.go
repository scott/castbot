package connect

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"unicode/utf8"
)

const googleDrafts = `"[Gmail]/Drafts"`

// lineConn reads protocol lines. A server reply can echo the app password,
// so callers return a fixed error instead of the reply text.
type lineConn struct {
	conn net.Conn
	r    *bufio.Reader
}

func (c *lineConn) readLine() (string, error) {
	var b strings.Builder
	for {
		chunk, err := c.r.ReadSlice('\n')
		if len(chunk) > 0 {
			if b.Len()+len(chunk) > 1<<20 {
				return "", ErrUpstream
			}
			b.Write(chunk)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return "", err
		}
		return b.String(), nil
	}
}

func (c *lineConn) write(s string) error {
	if _, err := io.WriteString(c.conn, s); err != nil {
		return ErrUpstream
	}
	return nil
}

func (c *lineConn) close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

type imapSession struct {
	line    lineConn
	seq     int
	records []string
}

func openGoogleIMAP(ctx context.Context) (*imapSession, string, string, error) {
	email, secret, err := googleCreds(ctx)
	if err != nil {
		return nil, "", "", err
	}
	conn, err := googleTLS(ctx, googleIMAP, "993")
	if err != nil {
		return nil, "", "", err
	}
	googleDeadline(ctx, conn)
	sess := &imapSession{line: lineConn{conn: conn, r: bufio.NewReader(conn)}}
	if err := sess.greet(); err != nil {
		sess.line.close()
		return nil, "", "", err
	}
	user, err := imapQuote(email)
	if err != nil {
		sess.line.close()
		return nil, "", "", err
	}
	pass, err := imapQuote(secret)
	if err != nil {
		sess.line.close()
		return nil, "", "", err
	}
	if err := sess.cmd("LOGIN " + user + " " + pass); err != nil {
		sess.line.close()
		return nil, "", "", err
	}
	return sess, email, secret, nil
}

func (s *imapSession) close() { s.line.close() }

func (s *imapSession) greet() error {
	rec, err := s.line.readLine()
	if err != nil || !strings.Contains(strings.ToUpper(rec), "OK") {
		return ErrUpstream
	}
	return nil
}

func (s *imapSession) cmd(line string) error {
	if googleBanned(line) {
		return ErrBad
	}
	s.seq++
	tag := fmt.Sprintf("a%d", s.seq)
	if err := s.line.write(tag + " " + line + "\r\n"); err != nil {
		return err
	}
	s.records = nil
	for len(s.records) < 400 {
		rec, err := s.readRecord()
		if err != nil {
			return ErrUpstream
		}
		if strings.HasPrefix(rec, tag+" ") {
			rest := strings.TrimSpace(rec[len(tag):])
			if strings.HasPrefix(strings.ToUpper(rest), "OK") {
				return nil
			}
			return ErrUpstream
		}
		s.records = append(s.records, rec)
	}
	return ErrUpstream
}

func (s *imapSession) readRecord() (string, error) {
	line, err := s.line.readLine()
	if err != nil {
		return "", err
	}
	n, ok := trailingLiteral(line)
	if !ok {
		return line, nil
	}
	if n > 1<<20 || len(line)+n > 1<<20 {
		return "", ErrUpstream
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(s.line.r, buf); err != nil {
		return "", err
	}
	return line + string(buf), nil
}

// appendDraft waits for the continuation, then writes an exact literal.
// The generic command reader cannot do that step.
func (s *imapSession) appendDraft(msg string) error {
	if strings.Contains(msg, "\x00") {
		return ErrBad
	}
	s.seq++
	tag := fmt.Sprintf("a%d", s.seq)
	if err := s.line.write(fmt.Sprintf("%s APPEND %s {%d}\r\n", tag, googleDrafts, len(msg))); err != nil {
		return err
	}
	continued := false
	for i := 0; i < 20; i++ {
		rec, err := s.line.readLine()
		if err != nil {
			return ErrUpstream
		}
		trim := strings.TrimSpace(rec)
		if strings.HasPrefix(trim, "+") {
			continued = true
			break
		}
		if strings.HasPrefix(trim, tag+" ") {
			return ErrUpstream
		}
	}
	if !continued {
		return ErrUpstream
	}
	if err := s.line.write(msg); err != nil {
		return err
	}
	for i := 0; i < 40; i++ {
		rec, err := s.readRecord()
		if err != nil {
			return ErrUpstream
		}
		if strings.HasPrefix(rec, tag+" ") {
			rest := strings.TrimSpace(rec[len(tag):])
			if strings.HasPrefix(strings.ToUpper(rest), "OK") {
				return nil
			}
			return ErrUpstream
		}
	}
	return ErrUpstream
}

func trailingLiteral(line string) (int, bool) {
	s := strings.TrimRight(line, "\r\n")
	if !strings.HasSuffix(s, "}") {
		return 0, false
	}
	i := strings.LastIndex(s, "{")
	if i < 0 || i+1 >= len(s)-1 {
		return 0, false
	}
	n := 0
	for _, r := range s[i+1 : len(s)-1] {
		if r < '0' || r > '9' || n > 1<<20 {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

func splitLiteral(rec string) string {
	line := rec
	if i := strings.Index(rec, "\n"); i >= 0 {
		line = rec[:i+1]
	}
	n, ok := trailingLiteral(line)
	if !ok || len(rec) < len(line)+n {
		return ""
	}
	return rec[len(line) : len(line)+n]
}

func imapQuote(s string) (string, error) {
	if s == "" || strings.ContainsAny(s, "\"\\\r\n\x00") {
		return "", ErrBad
	}
	return `"` + s + `"`, nil
}

func googleBanned(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "DELETE", "EXPUNGE", "STORE", "SETACL":
		return true
	default:
		return false
	}
}

func googleListMessages(ctx context.Context, args map[string]any) ([]byte, error) {
	limit, err := googleLimit(args, "limit", 10, 20)
	if err != nil {
		return nil, err
	}
	sess, _, secret, err := openGoogleIMAP(ctx)
	if err != nil {
		return nil, err
	}
	defer sess.close()
	if err := sess.cmd("EXAMINE INBOX"); err != nil {
		return nil, err
	}
	if err := sess.cmd("UID SEARCH ALL"); err != nil {
		return nil, err
	}
	uids := googleSearchUIDs(sess.records)
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
	}
	msgs := []googleListed{}
	if len(uids) > 0 {
		if err := sess.cmd("UID FETCH " + strings.Join(uids, ",") + " (BODY.PEEK[HEADER.FIELDS (FROM SUBJECT DATE)])"); err != nil {
			return nil, err
		}
		msgs = googleListedFrom(sess.records)
	}
	return googleJSON(secret, map[string]any{"messages": msgs})
}

func googleGetMessage(ctx context.Context, args map[string]any) ([]byte, error) {
	uid, err := googleUID(args)
	if err != nil {
		return nil, err
	}
	sess, _, secret, err := openGoogleIMAP(ctx)
	if err != nil {
		return nil, err
	}
	defer sess.close()
	if err := sess.cmd("EXAMINE INBOX"); err != nil {
		return nil, err
	}
	if err := sess.cmd("UID FETCH " + uid + " (BODYSTRUCTURE BODY.PEEK[HEADER])"); err != nil {
		return nil, err
	}
	from, to, subject, date := googleHeaders(googleHeaderLiteral(sess.records))
	text := "The message body could not be read."
	atts := []googleAtt{}
	if raw, ok := googleStructureFrom(sess.records); ok {
		if parts, err := googleBodyParts(raw); err == nil {
			picked := googlePickText(parts)
			atts = googleAttachments(parts, picked.section)
			if picked.section != "" && googleSection(picked.section) {
				if err := sess.cmd("UID FETCH " + uid + " (BODY.PEEK[" + picked.section + "]<0.65536>)"); err != nil {
					return nil, err
				}
				text = googleMessageText(picked, googleLiteral(sess.records))
			}
		}
	}
	return googleJSON(secret, googleGot{
		UID: uid, From: clipText(from, 300), To: clipText(to, 300),
		Subject: clipText(subject, 300), Date: clipText(date, 120),
		Text: clipText(text, googleTextCap), Attachments: atts,
	})
}

func googleCreateDraft(ctx context.Context, args map[string]any) ([]byte, error) {
	to, subject, body, err := googleCompose(args)
	if err != nil {
		return nil, err
	}
	sess, email, secret, err := openGoogleIMAP(ctx)
	if err != nil {
		return nil, err
	}
	defer sess.close()
	msg := googleRFC822(email, to, subject, body)
	if strings.Contains(msg, secret) {
		return nil, ErrBad
	}
	if err := sess.appendDraft(msg); err != nil {
		return nil, err
	}
	return googleJSON(secret, map[string]any{"draft": true})
}

func googleSend(ctx context.Context, args map[string]any) ([]byte, error) {
	to, subject, body, err := googleCompose(args)
	if err != nil {
		return nil, err
	}
	email, secret, err := googleCreds(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := googleTLS(ctx, googleSMTP, "465")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	googleDeadline(ctx, conn)
	smtp := &lineConn{conn: conn, r: bufio.NewReader(conn)}
	if err := smtp.expect(200, 299); err != nil {
		return nil, err
	}
	if err := smtp.write("EHLO castbot\r\n"); err != nil {
		return nil, err
	}
	if err := smtp.expect(200, 299); err != nil {
		return nil, err
	}
	plain := base64.StdEncoding.EncodeToString([]byte("\x00" + email + "\x00" + secret))
	if err := smtp.write("AUTH PLAIN " + plain + "\r\n"); err != nil {
		return nil, err
	}
	if err := smtp.expect(200, 299); err != nil {
		return nil, err
	}
	if err := smtp.write("MAIL FROM:<" + email + ">\r\n"); err != nil {
		return nil, err
	}
	if err := smtp.expect(200, 299); err != nil {
		return nil, err
	}
	if err := smtp.write("RCPT TO:<" + to + ">\r\n"); err != nil {
		return nil, err
	}
	if err := smtp.expect(200, 299); err != nil {
		return nil, err
	}
	if err := smtp.write("DATA\r\n"); err != nil {
		return nil, err
	}
	if err := smtp.expect(300, 399); err != nil {
		return nil, err
	}
	msg := googleRFC822(email, to, subject, body)
	if strings.Contains(msg, secret) {
		return nil, ErrBad
	}
	if err := smtp.write(smtpDot(msg)); err != nil {
		return nil, err
	}
	if err := smtp.expect(200, 299); err != nil {
		return nil, err
	}
	_ = smtp.write("QUIT\r\n")
	return googleJSON(secret, map[string]any{"sent": true})
}

func (c *lineConn) expect(min, max int) error {
	code, err := c.readReply()
	if err != nil || code < min || code > max {
		return ErrUpstream
	}
	return nil
}

func (c *lineConn) readReply() (int, error) {
	code := 0
	for i := 0; i < 40; i++ {
		line, err := c.readLine()
		if err != nil || len(line) < 4 {
			return 0, ErrUpstream
		}
		n := 0
		for i := 0; i < 3; i++ {
			if line[i] < '0' || line[i] > '9' {
				return 0, ErrUpstream
			}
			n = n*10 + int(line[i]-'0')
		}
		code = n
		if line[3] == ' ' {
			return code, nil
		}
		if line[3] != '-' {
			return 0, ErrUpstream
		}
	}
	return code, ErrUpstream
}

func googleCompose(args map[string]any) (to, subject, body string, err error) {
	rawTo, ok := argString(args, "to")
	if !ok {
		return "", "", "", ErrBad
	}
	to, err = googleOneAddress(rawTo)
	if err != nil {
		return "", "", "", err
	}
	subject, err = googleOneLine(args, "subject", 200)
	if err != nil {
		return "", "", "", err
	}
	rawBody, ok := argString(args, "body")
	if !ok {
		return "", "", "", ErrBad
	}
	body, err = googleBody(rawBody)
	if err != nil {
		return "", "", "", err
	}
	return to, subject, body, nil
}

func googleOneAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, ",") || !googleEmail(s) {
		return "", ErrBad
	}
	return s, nil
}

func googleBody(s string) (string, error) {
	if s == "" || strings.ContainsRune(s, 0) {
		return "", ErrBad
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if len(s) > googleTextCap {
		return "", ErrBad
	}
	return strings.ReplaceAll(s, "\n", "\r\n"), nil
}

func googleRFC822(from, to, subject, body string) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\r\n") {
		b.WriteString("\r\n")
	}
	return b.String()
}

func smtpDot(msg string) string {
	if !strings.HasSuffix(msg, "\r\n") {
		msg += "\r\n"
	}
	lines := strings.Split(msg, "\r\n")
	var b strings.Builder
	for i, line := range lines {
		if i == len(lines)-1 {
			break
		}
		if strings.HasPrefix(line, ".") {
			b.WriteByte('.')
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	b.WriteString(".\r\n")
	return b.String()
}

func googleUID(args map[string]any) (string, error) {
	uid, ok := argString(args, "uid")
	if !ok || !googleDigits(uid) {
		return "", ErrBad
	}
	return uid, nil
}

func googleDigits(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func googleSearchUIDs(records []string) []string {
	var uids []string
	for _, rec := range records {
		trim := strings.TrimSpace(rec)
		if !strings.HasPrefix(strings.ToUpper(trim), "* SEARCH") {
			continue
		}
		fields := strings.Fields(trim)
		if len(fields) < 3 {
			continue
		}
		for _, field := range fields[2:] {
			if googleDigits(field) {
				uids = append(uids, field)
			}
		}
	}
	return uids
}

type googleListed struct {
	UID     string `json:"uid"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
}

func googleListedFrom(records []string) []googleListed {
	out := []googleListed{}
	for _, rec := range records {
		uid := googleFetchUID(rec)
		block := splitLiteral(rec)
		if uid == "" || block == "" {
			continue
		}
		from, _, subject, date := googleHeaders(block)
		out = append(out, googleListed{
			UID: uid, From: clipText(from, 300), Subject: clipText(subject, 300), Date: clipText(date, 120),
		})
	}
	return out
}

func googleFetchUID(rec string) string {
	head := rec
	if i := strings.Index(rec, "{"); i >= 0 {
		head = rec[:i]
	}
	i := strings.Index(head, "UID ")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(head[i+4:])
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	if !googleDigits(rest[:n]) {
		return ""
	}
	return rest[:n]
}

func googleHeaderLiteral(records []string) string {
	for _, rec := range records {
		upper := strings.ToUpper(rec)
		if !strings.Contains(upper, "HEADER") {
			continue
		}
		if lit := splitLiteral(rec); lit != "" {
			return lit
		}
	}
	return ""
}

func googleLiteral(records []string) string {
	var b strings.Builder
	for _, rec := range records {
		b.WriteString(splitLiteral(rec))
	}
	return b.String()
}

func googleHeaders(block string) (from, to, subject, date string) {
	unfolded := googleUnfoldHeader(block)
	return googleHeader(unfolded, "From"), googleHeader(unfolded, "To"), googleHeader(unfolded, "Subject"), googleHeader(unfolded, "Date")
}

func googleUnfoldHeader(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && b.Len() > 0 {
			b.WriteByte(' ')
			b.WriteString(strings.TrimSpace(line))
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

func googleHeader(block, name string) string {
	prefix := strings.ToLower(name) + ":"
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

type googleGot struct {
	UID         string      `json:"uid"`
	From        string      `json:"from"`
	To          string      `json:"to"`
	Subject     string      `json:"subject"`
	Date        string      `json:"date"`
	Text        string      `json:"text"`
	Attachments []googleAtt `json:"attachments"`
}

type googleAtt struct {
	Filename string `json:"filename"`
	Type     string `json:"type"`
	Size     int    `json:"size"`
}

func googleMessageText(part mailPart, raw string) string {
	raw = clipBytes(raw, googleTextCap)
	if strings.EqualFold(part.subtype, "HTML") || googleLooksHTML(raw) {
		text := pageText([]byte(raw))
		if text == "" {
			return "No text."
		}
		return text
	}
	text := clipText(raw, googleTextCap)
	if text == "" {
		return "No text."
	}
	return text
}

func clipBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func googleLooksHTML(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(t, "<!doctype html") || strings.HasPrefix(t, "<html") || strings.Contains(t, "<body")
}

func googleJSON(secret string, v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, ErrUpstream
	}
	return []byte(Redact(secret, string(raw))), nil
}

type mailPart struct {
	section string
	media   string
	subtype string
	name    string
	size    int
	kids    []mailPart
}

// googleBodyParts numbers a single-part message as 1.
// A multipart root numbers its children 1 and 2. A nested part is 1.1.
func googleBodyParts(s string) ([]mailPart, error) {
	toks, err := tokenizeBody(strings.TrimSpace(s))
	if err != nil || len(toks) == 0 {
		return nil, ErrBad
	}
	part, next, err := parseBodyNode(toks, 0, "")
	if err != nil || next != len(toks) {
		return nil, ErrBad
	}
	return flattenParts(part), nil
}

func googlePickText(parts []mailPart) mailPart {
	var html mailPart
	for _, part := range parts {
		if part.media != "TEXT" {
			continue
		}
		if part.subtype == "PLAIN" {
			return part
		}
		if html.section == "" {
			html = part
		}
	}
	return html
}

func googleAttachments(parts []mailPart, textSection string) []googleAtt {
	out := []googleAtt{}
	for _, part := range parts {
		if part.section == textSection || part.media == "" {
			continue
		}
		if part.name == "" && part.media == "TEXT" {
			continue
		}
		out = append(out, googleAtt{
			Filename: clipText(part.name, 200),
			Type:     part.media + "/" + part.subtype,
			Size:     part.size,
		})
	}
	return out
}

func googleSection(s string) bool {
	if s == "" || len(s) > 40 || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	dot := false
	for _, r := range s {
		if r == '.' {
			if dot {
				return false
			}
			dot = true
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
		dot = false
	}
	return true
}

func flattenParts(part mailPart) []mailPart {
	if len(part.kids) == 0 {
		if part.media == "" {
			return nil
		}
		return []mailPart{part}
	}
	var out []mailPart
	for _, kid := range part.kids {
		out = append(out, flattenParts(kid)...)
	}
	return out
}

type bodyTok struct {
	kind byte
	val  string
	num  int
}

const (
	tokLP   byte = '('
	tokRP   byte = ')'
	tokNil  byte = 'N'
	tokStr  byte = 'S'
	tokNum  byte = 'D'
	tokAtom byte = 'A'
)

func tokenizeBody(s string) ([]bodyTok, error) {
	var out []bodyTok
	for i := 0; i < len(s); {
		switch s[i] {
		case ' ', '\t', '\r', '\n':
			i++
		case '(':
			out = append(out, bodyTok{kind: tokLP})
			i++
		case ')':
			out = append(out, bodyTok{kind: tokRP})
			i++
		case '"':
			var b strings.Builder
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' && j+1 < len(s) {
					b.WriteByte(s[j+1])
					j += 2
					continue
				}
				if s[j] == '"' {
					break
				}
				b.WriteByte(s[j])
				j++
			}
			if j >= len(s) || s[j] != '"' {
				return nil, ErrBad
			}
			out = append(out, bodyTok{kind: tokStr, val: b.String()})
			i = j + 1
		default:
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '(' && s[j] != ')' && s[j] != '"' && s[j] != '\r' && s[j] != '\n' && s[j] != '\t' {
				j++
			}
			word := s[i:j]
			if word == "" {
				return nil, ErrBad
			}
			if strings.EqualFold(word, "NIL") {
				out = append(out, bodyTok{kind: tokNil})
			} else if n, ok := bodyNumber(word); ok {
				out = append(out, bodyTok{kind: tokNum, val: word, num: n})
			} else {
				out = append(out, bodyTok{kind: tokAtom, val: word})
			}
			i = j
		}
	}
	return out, nil
}

func bodyNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' || n > 1<<30 {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

func parseBodyNode(toks []bodyTok, i int, section string) (mailPart, int, error) {
	if i >= len(toks) || toks[i].kind != tokLP {
		return mailPart{}, i, ErrBad
	}
	i++
	if i >= len(toks) {
		return mailPart{}, i, ErrBad
	}
	if toks[i].kind == tokLP {
		var kids []mailPart
		n := 1
		for i < len(toks) && toks[i].kind == tokLP {
			sec := fmt.Sprintf("%d", n)
			if section != "" {
				sec = section + "." + sec
			}
			kid, next, err := parseBodyNode(toks, i, sec)
			if err != nil {
				return mailPart{}, i, err
			}
			kids = append(kids, kid)
			i = next
			n++
		}
		if i >= len(toks) || toks[i].kind != tokStr {
			return mailPart{}, i, ErrBad
		}
		i++
		depth := 1
		for i < len(toks) && depth > 0 {
			switch toks[i].kind {
			case tokLP:
				depth++
			case tokRP:
				depth--
			}
			i++
		}
		if depth != 0 {
			return mailPart{}, i, ErrBad
		}
		return mailPart{section: section, kids: kids}, i, nil
	}
	if toks[i].kind != tokStr {
		return mailPart{}, i, ErrBad
	}
	media := strings.ToUpper(toks[i].val)
	i++
	if i >= len(toks) || toks[i].kind != tokStr {
		return mailPart{}, i, ErrBad
	}
	subtype := strings.ToUpper(toks[i].val)
	i++
	name := ""
	if i < len(toks) && toks[i].kind == tokNil {
		i++
	} else if i < len(toks) && toks[i].kind == tokLP {
		i++
		for i < len(toks) && toks[i].kind != tokRP {
			if i+1 < len(toks) && toks[i].kind == tokStr && toks[i+1].kind == tokStr {
				key := strings.ToUpper(toks[i].val)
				if (key == "NAME" || key == "FILENAME") && (name == "" || key == "FILENAME") {
					name = toks[i+1].val
				}
				i += 2
				continue
			}
			if toks[i].kind == tokLP {
				depth := 1
				i++
				for i < len(toks) && depth > 0 {
					switch toks[i].kind {
					case tokLP:
						depth++
					case tokRP:
						depth--
					}
					i++
				}
				continue
			}
			i++
		}
		if i < len(toks) && toks[i].kind == tokRP {
			i++
		}
	}
	size := 0
	saw := false
	for i < len(toks) && toks[i].kind != tokRP && !saw {
		if toks[i].kind == tokNum {
			size = toks[i].num
			saw = true
			i++
			break
		}
		if toks[i].kind == tokLP {
			depth := 1
			i++
			for i < len(toks) && depth > 0 {
				switch toks[i].kind {
				case tokLP:
					depth++
				case tokRP:
					depth--
				}
				i++
			}
			continue
		}
		i++
	}
	depth := 1
	for i < len(toks) && depth > 0 {
		switch toks[i].kind {
		case tokLP:
			depth++
		case tokRP:
			depth--
		}
		i++
	}
	if depth != 0 {
		return mailPart{}, i, ErrBad
	}
	if section == "" {
		section = "1"
	}
	return mailPart{section: section, media: media, subtype: subtype, name: name, size: size}, i, nil
}

func googleStructureFrom(records []string) (string, bool) {
	for _, rec := range records {
		if s, ok := googleStructure(rec); ok {
			return s, true
		}
	}
	return "", false
}

func googleStructure(rec string) (string, bool) {
	upper := strings.ToUpper(rec)
	const key = "BODYSTRUCTURE "
	i := strings.Index(upper, key)
	if i < 0 {
		return "", false
	}
	rest := strings.TrimLeft(rec[i+len(key):], " \t")
	if rest == "" || rest[0] != '(' {
		return "", false
	}
	depth := 0
	inQuote := false
	esc := false
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		if inQuote {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inQuote = false
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return rest[:j+1], true
			}
		}
	}
	return "", false
}
