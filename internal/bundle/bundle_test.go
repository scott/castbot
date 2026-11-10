package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveRoundTrip(t *testing.T) {
	raw, sum, err := Archive("name: Atlas\n", "Be brief.\n")
	if err != nil {
		t.Fatal(err)
	}
	if sum != Sum(raw) || len(sum) != 64 {
		t.Fatalf("sum %s", sum)
	}
	again, sum2, err := Archive("name: Atlas\n", "Be brief.\n")
	if err != nil {
		t.Fatal(err)
	}
	if sum2 != sum || string(again) != string(raw) {
		t.Fatal("archive is not stable")
	}
	dir := t.TempDir()
	note := filepath.Join(dir, "workspace", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(note), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("stay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir, raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil || string(got) != "name: Atlas\n" {
		t.Fatalf("yaml %q %v", got, err)
	}
	kept, err := os.ReadFile(note)
	if err != nil || string(kept) != "stay" {
		t.Fatal("install removed workspace")
	}
	if _, err := os.Stat(filepath.Join(dir, "memory")); err != nil {
		t.Fatal(err)
	}
}
