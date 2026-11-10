package castlet

import "testing"

func TestRequireHTTPS(t *testing.T) {
	if err := RequireHTTPS("https://castbot.example"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", "http://castbot.example", "https://", "castbot.example", "ftp://castbot.example/x"} {
		if err := RequireHTTPS(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if _, err := New(Config{ControlURL: "http://127.0.0.1:9", AgentID: "a"}); err == nil {
		t.Fatal("http control plane was accepted")
	}
}
