// Package runtime talks to a Cast A2A server.
// The HTTP client is the local implementation. S03 points it at castlet
// and S04 points it at a MicroVM. The run loop owns reconnects.
package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// HistoryTurns is the newest transcript entries sent on a first open.
	HistoryTurns = 50
	// HistoryBytes is the cap on the role and text of those entries.
	// The smaller of this and HistoryTurns wins.
	HistoryBytes = 100 << 10
)

// Turn is one saved transcript entry. Cast reads role and text.
type Turn struct {
	Role string
	Text string
}

// Send is one message/send, or a resume of one.
// Offset is the count of data events already consumed. Keepalive comments
// do not count. SessionID is the thread id and StreamID is the run id.
// MessageID is the stored user message. A retry sends the same id so the
// runtime session does not append that user turn again.
// History is the earlier transcript and is sent only on the first open.
// CapHistory keeps the last HistoryTurns entries or about 100 KB,
// whichever is smaller.
type Send struct {
	SessionID string
	StreamID  string
	MessageID string
	Offset    int
	Text      string
	History   []Turn
}

// StatusError is a non-200 response from the runtime.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("runtime status %d", e.Code)
}

// Frame is one SSE event from the runtime.
// DataEvent is true for a data frame, including "[DONE]".
// Comment frames, including keepalive and the cast-stream marker, are not data.
type Frame struct {
	Data      string
	Comment   string
	DataEvent bool
}

// Stream is one HTTP response. EOF from Next means this connection ended.
type Stream interface {
	Next() (Frame, error)
	Close() error
}

// Client opens one connection. It does not reconnect.
type Client interface {
	Open(ctx context.Context, req Send) (Stream, error)
}

// CapHistory keeps the newest turns that fit HistoryTurns and HistoryBytes.
// A single turn longer than the byte cap is cut to its newest bytes.
func CapHistory(in []Turn) []Turn {
	if len(in) == 0 {
		return nil
	}
	if len(in) > HistoryTurns {
		in = in[len(in)-HistoryTurns:]
	}
	turns := append([]Turn(nil), in...)
	n := 0
	for _, turn := range turns {
		n += len(turn.Role) + len(turn.Text)
	}
	for len(turns) > 1 && n > HistoryBytes {
		n -= len(turns[0].Role) + len(turns[0].Text)
		turns = turns[1:]
	}
	if len(turns) == 1 {
		room := HistoryBytes - len(turns[0].Role)
		if room < 0 {
			room = 0
		}
		if len(turns[0].Text) > room {
			text := turns[0].Text[len(turns[0].Text)-room:]
			for len(text) > 0 && !utf8.ValidString(text) {
				text = text[1:]
			}
			turns[0].Text = text
		}
	}
	return turns
}

// HTTPClient is a Cast A2A client over HTTP.
type HTTPClient struct {
	base   string
	token  string
	edge   string
	client *http.Client
}

// SetToken sends X-Cast-Authorization on later opens.
// Set it before the first open. The value is not logged.
func (c *HTTPClient) SetToken(token string) { c.token = token }

// SetEdgeToken sends Authorization for the machine edge.
// The machine API strips this header before the guest sees it.
// The value is not logged.
func (c *HTTPClient) SetEdgeToken(token string) { c.edge = token }

// NewHTTP returns a client for one runtime base URL.
func NewHTTP(base string) *HTTPClient {
	return &HTTPClient{
		base: strings.TrimRight(base, "/"),
		client: &http.Client{Transport: &http.Transport{
			DisableKeepAlives:     true,
			DisableCompression:    true,
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		}},
	}
}

// Open posts message/send with Accept: text/event-stream.
func (c *HTTPClient) Open(ctx context.Context, req Send) (Stream, error) {
	meta := map[string]any{
		"sessionId":    req.SessionID,
		"streamId":     req.StreamID,
		"streamOffset": req.Offset,
	}
	history := CapHistory(req.History)
	if req.Offset == 0 && len(history) > 0 {
		turns := make([]map[string]string, 0, len(history))
		for _, turn := range history {
			turns = append(turns, map[string]string{"role": turn.Role, "text": turn.Text})
		}
		meta["history"] = turns
	}
	messageID := req.MessageID
	if messageID == "" {
		messageID = req.StreamID
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      req.StreamID + ":" + strconv.Itoa(req.Offset),
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"messageId": messageID,
				"parts":     []map[string]string{{"kind": "text", "text": req.Text}},
			},
			"metadata": meta,
		},
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("X-Cast-Authorization", "Bearer "+c.token)
	}
	if c.edge != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.edge)
	}
	res, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, &StatusError{Code: res.StatusCode}
	}
	return &httpStream{body: res.Body, reader: bufio.NewReader(res.Body)}, nil
}

// Respond posts permission/respond. allow false is Deny.
func (c *HTTPClient) Respond(ctx context.Context, requestID string, allow bool) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errors.New("missing request id")
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  "permission/respond",
		"params": map[string]any{
			"id":       requestID,
			"approved": allow,
		},
	})
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.token != "" {
		httpReq.Header.Set("X-Cast-Authorization", "Bearer "+c.token)
	}
	if c.edge != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.edge)
	}
	res, err := c.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusAccepted {
		return &StatusError{Code: res.StatusCode}
	}
	return PermissionError(raw)
}

type httpStream struct {
	body   io.ReadCloser
	reader *bufio.Reader
	buf    strings.Builder
}

func (s *httpStream) Close() error { return s.body.Close() }

func (s *httpStream) Next() (Frame, error) {
	for {
		if block, ok := takeBlock(&s.buf); ok {
			if strings.TrimSpace(block) == "" {
				continue
			}
			return parseBlock(block), nil
		}
		chunk := make([]byte, 1024)
		n, err := s.reader.Read(chunk)
		if n > 0 {
			s.buf.Write(chunk[:n])
		}
		if err != nil {
			if err == io.EOF {
				if s.buf.Len() == 0 {
					return Frame{}, io.EOF
				}
				return Frame{}, io.ErrUnexpectedEOF
			}
			return Frame{}, err
		}
	}
}

func takeBlock(buf *strings.Builder) (string, bool) {
	raw := buf.String()
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	i := strings.Index(raw, "\n\n")
	if i < 0 {
		buf.Reset()
		buf.WriteString(raw)
		return "", false
	}
	block := raw[:i]
	rest := raw[i+2:]
	buf.Reset()
	buf.WriteString(rest)
	return block, true
}

func parseBlock(block string) Frame {
	var data []string
	var comments []string
	dataEvent := false
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			comments = append(comments, strings.TrimSpace(strings.TrimPrefix(line, ":")))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataEvent = true
			v := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(v, " ") {
				v = v[1:]
			}
			data = append(data, v)
		}
	}
	return Frame{
		Data:      strings.Join(data, "\n"),
		Comment:   strings.Join(comments, "\n"),
		DataEvent: dataEvent,
	}
}

// TextParts returns the text parts of one JSON-RPC data frame.
// done is true for the terminal [DONE] marker.
// rpcErr is set when the frame is a JSON-RPC error.
type Parsed struct {
	Text    string
	Perm    json.RawMessage
	RPCErr  string
	Done    bool
	Ignored bool
}

// ParseData interprets one data payload the way the Cast client does.
func ParseData(data string) (Parsed, error) {
	if data == "[DONE]" {
		return Parsed{Done: true}, nil
	}
	var frame struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result *struct {
			Parts []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"parts"`
			Metadata struct {
				Permission json.RawMessage `json:"permissionRequest"`
			} `json:"metadata"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return Parsed{Ignored: true}, nil
	}
	if frame.Error != nil {
		msg := strings.TrimSpace(frame.Error.Message)
		if msg == "" {
			msg = "runtime error"
		}
		return Parsed{RPCErr: msg}, nil
	}
	out := Parsed{}
	if frame.Result != nil {
		var b strings.Builder
		for _, part := range frame.Result.Parts {
			if part.Kind == "text" {
				b.WriteString(part.Text)
			}
		}
		out.Text = b.String()
		if len(frame.Result.Metadata.Permission) > 0 && string(frame.Result.Metadata.Permission) != "null" {
			out.Perm = frame.Result.Metadata.Permission
		}
	}
	return out, nil
}

// ErrNoRequest means permission/respond did not match a waiting prompt.
var ErrNoRequest = errors.New("no pending permission request")

// DenialMessage is the tool result Cast returns when an ask prompt is denied.
func DenialMessage(tool string) string {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "tool"
	}
	return "Error: Permission denied for " + tool + ". Approval is required before this tool can run, and it was not executed."
}

// PermissionError reads a permission/respond body.
// An empty body is success. A missing prompt is ErrNoRequest.
// Other error text is not returned, so a body cannot leak into logs.
func PermissionError(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var frame struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &frame) != nil || frame.Error == nil {
		return nil
	}
	msg := strings.ToLower(strings.TrimSpace(frame.Error.Message))
	if strings.Contains(msg, "no pending") {
		return ErrNoRequest
	}
	return errors.New("permission was not accepted")
}
