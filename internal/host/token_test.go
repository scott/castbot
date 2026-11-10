package host

import (
	"testing"
)

func TestDOTokenIgnoresOtherVariables(t *testing.T) {
	t.Setenv("CASTBOT_DO_TOKEN", "")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "other-provider-token")
	t.Setenv("DIGITALOCEAN_TOKEN", "another-provider-token")
	if DOToken() != "" {
		t.Fatal("a different DigitalOcean variable was used")
	}
	t.Setenv("CASTBOT_DO_TOKEN", "castbot-only")
	if DOToken() != "castbot-only" {
		t.Fatal(DOToken())
	}
}
