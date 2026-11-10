// Package runtime talks to a Cast A2A server.
// The HTTP client is the local implementation. S03 points it at castlet
// and S04 points it at a MicroVM. The run loop owns reconnects.
package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Send is one message/send, or a resume of one.
// Offset is the count of data events already consumed. Keepalive comments
// do not count. SessionID is the thread id and StreamID is the run id.
type Send struct {
	SessionID string
	StreamID  string
	Offset    int
	Text      string
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

// HTTPClient is a Cast A2A client over HTTP.
type HTTPClient struct {
	base   string
	client *http.Client
}

// NewHTTP returns a client for one runtime base URL.
func NewHTTP(base string) *HTTPClient {
	return &HTTPClient{
		base: strings.TrimRight(base, "/"),
		client: &http.Client{Transport: &http.Transport{
			DisableKeepAlives:  true,
			DisableCompression: true,
			Proxy:              http.ProxyFromEnvironment,
		}},
	}
}

// Open posts message/send with Accept: text/event-stream.
func (c *HTTPClient) Open(ctx context.Context, req Send) (Stream, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      req.StreamID + ":" + strconv.Itoa(req.Offset),
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"messageId": req.StreamID,
				"parts":     []map[string]string{{"kind": "text", "text": req.Text}},
			},
			"metadata": map[string]any{
				"sessionId":    req.SessionID,
				"streamId":     req.StreamID,
				"streamOffset": req.Offset,
			},
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
	res, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("runtime status %d", res.StatusCode)
	}
	return &httpStream{body: res.Body, reader: bufio.NewReader(res.Body)}, nil
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
