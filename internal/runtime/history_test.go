package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCapHistoryKeepsTheNewestBound(t *testing.T) {
	turns := make([]Turn, 60)
	for i := range turns {
		turns[i] = Turn{Role: "user", Text: strings.Repeat("x", 3000)}
	}
	got := CapHistory(turns)
	if len(got) > HistoryTurns {
		t.Fatalf("turns %d", len(got))
	}
	n := 0
	for _, turn := range got {
		n += len(turn.Role) + len(turn.Text)
	}
	if n > HistoryBytes {
		t.Fatalf("bytes %d", n)
	}
	if len(got) == 0 || got[len(got)-1].Text != turns[len(turns)-1].Text {
		t.Fatal("dropped the newest turn")
	}
	short := CapHistory([]Turn{{Role: "user", Text: "hello"}, {Role: "agent", Text: "hi"}})
	if len(short) != 2 || short[0].Text != "hello" || short[1].Text != "hi" {
		t.Fatalf("short %#v", short)
	}
}

func TestOpenSendsCappedHistoryOnce(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": cast-stream r\n\n")
	}))
	defer srv.Close()
	turns := make([]Turn, 60)
	for i := range turns {
		turns[i] = Turn{Role: "agent", Text: strings.Repeat("y", 3000)}
	}
	client := NewHTTP(srv.URL)
	stream, err := client.Open(context.Background(), Send{SessionID: "s", StreamID: "r", Text: "hi", History: turns})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	var msg struct {
		Params struct {
			Metadata struct {
				History []struct {
					Role string `json:"role"`
					Text string `json:"text"`
				} `json:"history"`
			} `json:"metadata"`
		} `json:"params"`
	}
	if err := json.Unmarshal(got, &msg); err != nil {
		t.Fatal(err)
	}
	history := msg.Params.Metadata.History
	if len(history) == 0 || len(history) > HistoryTurns {
		t.Fatalf("history %d", len(history))
	}
	n := 0
	for _, turn := range history {
		n += len(turn.Role) + len(turn.Text)
	}
	if n > HistoryBytes {
		t.Fatalf("bytes %d", n)
	}
}
