package castlet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPartialTrailingLineStaysReadable(t *testing.T) {
	dir := t.TempDir()
	first, err := New(Config{AgentDir: dir, Token: "agent-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.appendJournal(journalEntry{Kind: "reply", Text: "kept"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".castlet", "journal.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(`{"seq":9,"kind":"reply","text":"torn`)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{AgentDir: dir, Token: "agent-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.appendJournal(journalEntry{Kind: "reply", Text: "after"}); err != nil {
		t.Fatal(err)
	}
	got := second.journalAfter(0)
	if len(got) != 2 {
		t.Fatalf("entries %d", len(got))
	}
	if got[0].Seq >= got[1].Seq || got[0].Text != "kept" || got[1].Text != "after" {
		t.Fatalf("seq %d %d", got[0].Seq, got[1].Seq)
	}
}
