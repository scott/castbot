package runtime

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyCutsTheBody(t *testing.T) {
	echo := NewEcho(EchoOptions{Name: "echo", WordDelay: 20 * time.Millisecond})
	up := httptest.NewServer(echo)
	defer up.Close()
	defer echo.Close()
	proxy := NewCutProxy(up.URL, 150*time.Millisecond)
	px := httptest.NewServer(proxy)
	defer px.Close()
	client := NewHTTP(px.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.Open(ctx, Send{SessionID: "thread", StreamID: "cut1", Text: strings.Repeat("alpha ", 30)})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	start := time.Now()
	var n int
	var sawDone bool
	for {
		frame, err := stream.Next()
		if err != nil {
			break
		}
		if !frame.DataEvent {
			continue
		}
		n++
		parsed, err := ParseData(frame.Data)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Done {
			sawDone = true
			break
		}
	}
	if sawDone {
		t.Fatal("full reply crossed the cut")
	}
	if n == 0 {
		t.Fatal("no events before the cut")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cut was late: %s", time.Since(start))
	}
	if proxy.Cuts() < 1 {
		t.Fatal("cut was not counted")
	}
}
