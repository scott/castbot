package runtime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCastTimingDefaults(t *testing.T) {
	if KeepaliveInterval != 5*time.Second {
		t.Fatal(KeepaliveInterval)
	}
	if ResumeRetain != 120*time.Second {
		t.Fatal(ResumeRetain)
	}
	if DefaultCut != 15*time.Second {
		t.Fatal(DefaultCut)
	}
	if !strings.HasPrefix(KeepaliveFrame, ": keepalive ") || !strings.HasSuffix(KeepaliveFrame, "\n\n") {
		t.Fatal("keepalive frame")
	}
}

func TestEchoResumeSkipsDeliveredEvents(t *testing.T) {
	echo := NewEcho(EchoOptions{Name: "echo", WordDelay: 5 * time.Millisecond})
	up := httptest.NewServer(echo)
	defer up.Close()
	defer echo.Close()
	client := NewHTTP(up.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	want := EchoText("echo", "hello there")
	stream, err := client.Open(ctx, Send{SessionID: "thread", StreamID: "run1", Text: "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	offset := 0
	for {
		frame, err := stream.Next()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(frame.Comment, "cast-stream ") {
			continue
		}
		if !frame.DataEvent {
			continue
		}
		offset++
		parsed, err := ParseData(frame.Data)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Text != "" {
			got = append(got, parsed.Text)
		}
		if len(got) >= 3 {
			stream.Close()
			break
		}
	}
	if echo.Starts() != 1 {
		t.Fatal(echo.Starts())
	}
	rest, err := client.Open(ctx, Send{SessionID: "thread", StreamID: "run1", Offset: offset, Text: "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	defer rest.Close()
	for {
		frame, err := rest.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !frame.DataEvent {
			continue
		}
		parsed, err := ParseData(frame.Data)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Done {
			break
		}
		if parsed.Text != "" {
			got = append(got, parsed.Text)
		}
	}
	if strings.Join(got, "") != want {
		t.Fatalf("got %q want %q", strings.Join(got, ""), want)
	}
	if echo.Starts() != 1 {
		t.Fatalf("second producer %d", echo.Starts())
	}
}

func TestEchoKeepaliveAndRetain(t *testing.T) {
	echo := NewEcho(EchoOptions{Name: "echo", WordDelay: time.Second, Keepalive: 40 * time.Millisecond, Retain: 80 * time.Millisecond})
	up := httptest.NewServer(echo)
	defer up.Close()
	defer echo.Close()
	client := NewHTTP(up.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := client.Open(ctx, Send{SessionID: "thread", StreamID: "quiet", Text: "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	saw := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		frame, err := stream.Next()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(frame.Comment, "keepalive") {
			saw = true
			break
		}
	}
	stream.Close()
	if !saw {
		t.Fatal("no keepalive")
	}

	fast := NewEcho(EchoOptions{Name: "echo", Retain: 80 * time.Millisecond})
	fastUp := httptest.NewServer(fast)
	defer fastUp.Close()
	defer fast.Close()
	fastClient := NewHTTP(fastUp.URL)
	if err := readUntilDone(ctx, fastClient, "again", 0); err != nil {
		t.Fatal(err)
	}
	if fast.Starts() != 1 {
		t.Fatal(fast.Starts())
	}
	if err := readUntilDone(ctx, fastClient, "again", 0); err != nil {
		t.Fatal(err)
	}
	if fast.Starts() != 1 {
		t.Fatalf("replay started a producer: %d", fast.Starts())
	}
	time.Sleep(100 * time.Millisecond)
	if err := readUntilDone(ctx, fastClient, "again", 0); err != nil {
		t.Fatal(err)
	}
	if fast.Starts() != 2 {
		t.Fatalf("retained run was not dropped: %d", fast.Starts())
	}
}

func readUntilDone(ctx context.Context, client *HTTPClient, id string, offset int) error {
	stream, err := client.Open(ctx, Send{SessionID: "thread", StreamID: id, Offset: offset, Text: "hello there"})
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		frame, err := stream.Next()
		if err != nil {
			return err
		}
		if !frame.DataEvent {
			continue
		}
		parsed, err := ParseData(frame.Data)
		if err != nil {
			return err
		}
		if parsed.Done {
			return nil
		}
	}
}

func TestEchoCardAndOneShot(t *testing.T) {
	echo := NewEcho(EchoOptions{Name: "echo"})
	up := httptest.NewServer(echo)
	defer up.Close()
	defer echo.Close()
	res, err := http.Get(up.URL + "/.well-known/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), `"streaming":true`) {
		t.Fatalf("card %d %s", res.StatusCode, body)
	}
}
