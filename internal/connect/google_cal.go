package connect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func googleListEvents(ctx context.Context, args map[string]any) ([]byte, error) {
	start, end, err := googleRange(args)
	if err != nil {
		return nil, err
	}
	email, secret, err := googleCreds(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := googleHTTPS(ctx, "REPORT", googleCalURL(email, ""), "application/xml; charset=UTF-8", []byte(googleReport(start, end)))
	if err != nil {
		return nil, err
	}
	return googleJSON(secret, map[string]any{"events": googleEvents(string(raw))})
}

func googleCreateEvent(ctx context.Context, args map[string]any) ([]byte, error) {
	title, start, end, description, err := googleEventArgs(args)
	if err != nil {
		return nil, err
	}
	email, secret, err := googleCreds(ctx)
	if err != nil {
		return nil, err
	}
	uid, err := googleNewUID()
	if err != nil {
		return nil, err
	}
	body := googleICS(uid, title, description, start, end, time.Now().UTC())
	if _, err := googleHTTPS(ctx, http.MethodPut, googleCalURL(email, uid), "text/calendar; charset=UTF-8", []byte(body)); err != nil {
		return nil, err
	}
	return googleJSON(secret, map[string]any{"uid": uid, "created": true})
}

func googleRange(args map[string]any) (time.Time, time.Time, error) {
	startRaw, startOK := argString(args, "start")
	endRaw, endOK := argString(args, "end")
	if (!startOK || startRaw == "") && (!endOK || endRaw == "") {
		now := time.Now().UTC()
		return now, now.Add(14 * 24 * time.Hour), nil
	}
	if startRaw == "" || endRaw == "" {
		return time.Time{}, time.Time{}, ErrBad
	}
	return googleSpan(startRaw, endRaw, true)
}

func googleSpan(startRaw, endRaw string, cap bool) (time.Time, time.Time, error) {
	start, errStart := time.Parse(time.RFC3339, startRaw)
	end, errEnd := time.Parse(time.RFC3339, endRaw)
	if errStart != nil || errEnd != nil || !end.After(start) {
		return time.Time{}, time.Time{}, ErrBad
	}
	if cap && end.Sub(start) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, ErrBad
	}
	return start.UTC(), end.UTC(), nil
}

func googleEventArgs(args map[string]any) (title string, start, end time.Time, description string, err error) {
	title, err = googleOneLine(args, "title", 200)
	if err != nil {
		return "", time.Time{}, time.Time{}, "", err
	}
	startRaw, ok := argString(args, "start")
	if !ok || startRaw == "" {
		return "", time.Time{}, time.Time{}, "", ErrBad
	}
	endRaw, ok := argString(args, "end")
	if !ok || endRaw == "" {
		return "", time.Time{}, time.Time{}, "", ErrBad
	}
	start, end, err = googleSpan(startRaw, endRaw, false)
	if err != nil {
		return "", time.Time{}, time.Time{}, "", err
	}
	if raw, ok := argString(args, "description"); ok && raw != "" {
		if strings.ContainsRune(raw, 0) || len(raw) > 4000 {
			return "", time.Time{}, time.Time{}, "", ErrBad
		}
		description = strings.ReplaceAll(raw, "\r", "")
	}
	return title, start, end, description, nil
}

func googleOneLine(args map[string]any, key string, max int) (string, error) {
	raw, ok := argString(args, key)
	if !ok || raw == "" || len(raw) > max || strings.ContainsAny(raw, "\r\n\x00") {
		return "", ErrBad
	}
	return raw, nil
}

func googleNewUID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", ErrUpstream
	}
	return hex.EncodeToString(buf[:]) + "@castbot", nil
}

func googleCalURL(email, uid string) string {
	base := "https://" + googleCal + "/caldav/v2/" + url.PathEscape(email) + "/events/"
	if uid == "" {
		return base
	}
	return base + url.PathEscape(uid) + ".ics"
}

func googleReport(start, end time.Time) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><C:calendar-data/></D:prop>` +
		`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">` +
		`<C:time-range start="` + googleStamp(start) + `" end="` + googleStamp(end) + `"/>` +
		`</C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`
}

func googleStamp(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

func googleICS(uid, summary, description string, start, end, stamp time.Time) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//castbot//EN\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	b.WriteString("UID:" + uid + "\r\n")
	b.WriteString("DTSTAMP:" + googleStamp(stamp) + "\r\n")
	b.WriteString("DTSTART:" + googleStamp(start) + "\r\n")
	b.WriteString("DTEND:" + googleStamp(end) + "\r\n")
	b.WriteString("SUMMARY:" + googleICSText(summary) + "\r\n")
	if description != "" {
		b.WriteString("DESCRIPTION:" + googleICSText(description) + "\r\n")
	}
	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

func googleICSText(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, ";", `\;`)
	return s
}

type googleEvent struct {
	UID     string `json:"uid"`
	Summary string `json:"summary"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

func googleEvents(body string) []googleEvent {
	out := []googleEvent{}
	rest := body
	upper := strings.ToUpper(body)
	for len(out) < 40 {
		i := strings.Index(upper, "BEGIN:VEVENT")
		if i < 0 {
			break
		}
		rest = rest[i+len("BEGIN:VEVENT"):]
		upper = upper[i+len("BEGIN:VEVENT"):]
		j := strings.Index(upper, "END:VEVENT")
		if j < 0 {
			break
		}
		block := googleUnfoldICS(rest[:j])
		out = append(out, googleEvent{
			UID:     clipText(googleICSProp(block, "UID"), 200),
			Summary: clipText(googleICSProp(block, "SUMMARY"), 300),
			Start:   clipText(googleICSProp(block, "DTSTART"), 80),
			End:     clipText(googleICSProp(block, "DTEND"), 80),
		})
		rest = rest[j+len("END:VEVENT"):]
		upper = upper[j+len("END:VEVENT"):]
	}
	return out
}

func googleUnfoldICS(s string) string {
	s = strings.ReplaceAll(s, "\r\n ", "")
	s = strings.ReplaceAll(s, "\r\n\t", "")
	s = strings.ReplaceAll(s, "\n ", "")
	s = strings.ReplaceAll(s, "\n\t", "")
	return s
}

func googleICSProp(block, name string) string {
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) <= len(name) || !strings.EqualFold(line[:len(name)], name) {
			continue
		}
		switch line[len(name)] {
		case ':', ';':
		default:
			continue
		}
		if i := strings.Index(line, ":"); i >= 0 {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}
