package castcfg

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScrubJSONDropsSecretKeys(t *testing.T) {
	raw := json.RawMessage(`{"command":"ls","api_key":"demo-secret-value","max_tokens":16,"env":{"token":"abc","name":"atlas"}}`)
	clean, redacted, err := ScrubJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(clean)
	if strings.Contains(text, "demo-secret-value") || strings.Contains(text, "abc") || strings.Contains(text, "atlas") {
		t.Fatalf("secret survived %s", text)
	}
	if !strings.Contains(text, `"command":"ls"`) || !strings.Contains(text, `"max_tokens":16`) {
		t.Fatalf("kept %s", text)
	}
	if strings.Join(redacted, ",") != "api_key,env" {
		t.Fatalf("redacted %#v", redacted)
	}
}

func TestScrubJSONDropsCamelCaseAndBearer(t *testing.T) {
	raw := json.RawMessage(`{"command":"ls","privateKey":"pk-demo","accessKey":"ak-demo","sessionKey":"sk-demo","cookie":"ck-demo","passphrase":"pp-demo","auth":"au-demo","pass":"pw-demo","pwd":"pd-demo","note":"Bearer demo-token","max_tokens":4,"visible":"keep-me"}`)
	clean, redacted, err := ScrubJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(clean)
	for _, secret := range []string{"pk-demo", "ak-demo", "sk-demo", "ck-demo", "pp-demo", "au-demo", "pw-demo", "pd-demo", "demo-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %s survived in %s", secret, text)
		}
	}
	if !strings.Contains(text, `"command":"ls"`) || !strings.Contains(text, `"max_tokens":4`) || !strings.Contains(text, `"visible":"keep-me"`) {
		t.Fatalf("kept %s", text)
	}
	if strings.Join(redacted, ",") != "privateKey,accessKey,sessionKey,cookie,passphrase,auth,pass,pwd,note" {
		t.Fatalf("redacted %#v", redacted)
	}
}

func TestScrubJSONKeepsOrder(t *testing.T) {
	clean, _, err := ScrubJSON(json.RawMessage(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(clean) != `{"b":1,"a":2}` {
		t.Fatalf("order %s", clean)
	}
}
