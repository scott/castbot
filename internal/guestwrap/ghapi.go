package guestwrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/scott/castbot/internal/egresscontract"
)

// ghCall is one `gh api` invocation redirected at the local egress.
type ghCall struct {
	Base      string
	Method    string
	Path      string
	Query     string
	Headers   map[string]string
	Body      []byte
	InputPath string
}

func parseGHAPI(args []string) (ghCall, bool) {
	if len(args) == 0 || args[0] != "api" {
		return ghCall{}, false
	}
	rest := args[1:]
	call := ghCall{Method: http.MethodGet, Headers: map[string]string{}}
	var endpoint string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			if endpoint == "" && i+1 < len(rest) {
				endpoint = rest[i+1]
			}
			break
		}
		name, inline, value := flagValue(a)
		take := func() (string, bool) {
			if inline {
				return value, true
			}
			if i+1 >= len(rest) {
				return "", false
			}
			i++
			return rest[i], true
		}
		switch name {
		case "--method", "-X":
			v, ok := take()
			if !ok || v == "" {
				return ghCall{}, false
			}
			call.Method = strings.ToUpper(v)
		case "--header", "-H":
			v, ok := take()
			if !ok {
				return ghCall{}, false
			}
			key, val, ok := strings.Cut(v, ":")
			if !ok {
				return ghCall{}, false
			}
			key = strings.ToLower(strings.TrimSpace(key))
			if key == "" || droppedHeader(key) {
				continue
			}
			call.Headers[key] = strings.TrimSpace(val)
		case "--input":
			v, ok := take()
			if !ok || v == "" {
				return ghCall{}, false
			}
			call.InputPath = v
		case "--silent", "--verbose":
		case "--hostname":
			if _, ok := take(); !ok {
				return ghCall{}, false
			}
		default:
			if strings.HasPrefix(a, "-") {
				return ghCall{}, false
			}
			if endpoint != "" {
				return ghCall{}, false
			}
			endpoint = a
		}
	}
	path, query, ok := splitEndpoint(endpoint)
	if !ok {
		return ghCall{}, false
	}
	call.Path = path
	call.Query = query
	if call.Method == "" {
		call.Method = http.MethodGet
	}
	switch call.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return ghCall{}, false
	}
	return call, true
}

func flagValue(a string) (name string, inline bool, value string) {
	if strings.HasPrefix(a, "--") {
		name, value, inline = strings.Cut(a, "=")
		return name, inline, value
	}
	if strings.HasPrefix(a, "-") && len(a) > 2 {
		return a[:2], true, a[2:]
	}
	return a, false, ""
}

func splitEndpoint(endpoint string) (string, string, bool) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || strings.Contains(endpoint, "://") || strings.Contains(endpoint, "..") {
		return "", "", false
	}
	path, query, _ := strings.Cut(endpoint, "?")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.Contains(path, "\\") || strings.ContainsAny(query, "\r\n") {
		return "", "", false
	}
	return path, query, true
}

func droppedHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "host",
		"x-cast-authorization", "content-length", "transfer-encoding":
		return true
	default:
		return false
	}
}

func runGH(call ghCall, in io.Reader, out, errw io.Writer) int {
	if call.InputPath != "" {
		var raw []byte
		var err error
		if call.InputPath == "-" {
			raw, err = io.ReadAll(io.LimitReader(in, egresscontract.BodyCap+1))
		} else {
			raw, err = os.ReadFile(call.InputPath)
		}
		if err != nil || len(raw) > egresscontract.BodyCap {
			writeLine(errw, "castbot: "+egresscontract.Rejected)
			return 1
		}
		call.Body = raw
	}
	if len(call.Body) > 0 && call.Method == http.MethodGet {
		call.Method = http.MethodPost
	}
	if call.Base == "" {
		call.Base = DefaultEgress
	}
	payload := egresscontract.Forward{
		Provider: "github",
		Method:   call.Method,
		Path:     call.Path,
		Query:    call.Query,
		Headers:  call.Headers,
	}
	if payload.Headers == nil {
		payload.Headers = map[string]string{}
	}
	if len(call.Body) > 0 {
		payload.BodyB64 = base64.StdEncoding.EncodeToString(call.Body)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		writeLine(errw, "castbot: "+egresscontract.Rejected)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(call.Base, "/")+"/http", bytes.NewReader(raw))
	if err != nil {
		writeLine(errw, "castbot: "+egresscontract.NotConnected)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := localClient(60 * time.Second).Do(req)
	if err != nil {
		writeLine(errw, "castbot: "+egresscontract.NotConnected)
		return 1
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, egresscontract.BodyCap+1))
	if err != nil || len(body) > egresscontract.BodyCap {
		writeLine(errw, "castbot: "+egresscontract.Rejected)
		return 1
	}
	if res.StatusCode != http.StatusOK {
		var fail egresscontract.Failure
		_ = json.Unmarshal(body, &fail)
		writeLine(errw, "castbot: "+egresscontract.SafeMessage(fail.Error))
		return 1
	}
	var result egresscontract.ForwardResult
	if err := json.Unmarshal(body, &result); err != nil {
		writeLine(errw, "castbot: "+egresscontract.Rejected)
		return 1
	}
	if result.Status >= 300 && result.Status < 400 {
		writeLine(errw, "castbot: "+egresscontract.RedirectRefused)
		return 1
	}
	decoded := []byte(nil)
	if result.BodyB64 != "" {
		decoded, err = base64.StdEncoding.DecodeString(result.BodyB64)
		if err != nil {
			writeLine(errw, "castbot: "+egresscontract.Rejected)
			return 1
		}
	}
	if _, err := out.Write(decoded); err != nil {
		return 1
	}
	if len(decoded) == 0 || decoded[len(decoded)-1] != '\n' {
		_, _ = out.Write([]byte("\n"))
	}
	if result.Status >= 400 || result.Status == 0 {
		return 1
	}
	return 0
}

func writeLine(w io.Writer, text string) {
	_, _ = io.WriteString(w, text+"\n")
}

// localClient never uses an environment proxy and never follows a redirect.
func localClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
