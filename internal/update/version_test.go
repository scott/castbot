package update

import "testing"

func TestNewer(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		running   string
		candidate string
		want      bool
	}{
		{"dev", "0.20261124.1", false},
		{"", "0.20261124.1", false},
		{"0.0.0+" + sha, "0.20261124.1", true},
		{"v0.0.0+" + sha, "0.20261124.1", true},
		{"0.20261124.1", "0.20261124.1", false},
		{"v0.20261124.1", "0.20261124.1", false},
		{"0.20261124.1", "v0.20261124.2", true},
		{"0.20261124.2", "0.20261124.1", false},
		{"0.20261124.2", "0.20261124.10", true},
		{"0.20261124.9", "0.20261125.1", true},
		{"0.20261124.1", "dev", false},
		{"1.0.0", "0.20261124.1", false},
		{"0.20261124.1", "1.0.0", true},
		{"0.20261124.1", "0.20261124.1+" + sha, false},
		{"nope", "0.20261124.1", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.running, tc.candidate); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.running, tc.candidate, got, tc.want)
		}
	}
}

func TestResolveMode(t *testing.T) {
	if _, ok := ParseMode("sometimes"); ok {
		t.Fatal("invalid mode accepted")
	}
	if got := ResolveMode("off", "auto"); got != ModeOff {
		t.Fatalf("env %s", got)
	}
	if got := ResolveMode("", "auto"); got != ModeAuto {
		t.Fatalf("stored %s", got)
	}
	if got := ResolveMode("", "nope"); got != ModePrompt {
		t.Fatalf("default %s", got)
	}
	if got := ResolveMode("nope", "prompt"); got != ModePrompt {
		t.Fatalf("invalid env %s", got)
	}
}
