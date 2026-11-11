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
	for _, secret := range []string{"pk-demo", "ak-demo", "sk-demo", "ck-demo", "pp-demo", "pd-demo", "demo-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %s survived in %s", secret, text)
		}
	}
	for _, kept := range []string{`"command":"ls"`, `"max_tokens":4`, `"visible":"keep-me"`, `"note":"Bearer Hidden"`} {
		if !strings.Contains(text, kept) {
			t.Fatalf("missing %s in %s", kept, text)
		}
	}
	for _, gone := range []string{"au-demo", "pw-demo"} {
		if strings.Contains(text, gone) {
			t.Fatalf("showed %s in %s", gone, text)
		}
	}
	if strings.Join(redacted, ",") != "privateKey,accessKey,sessionKey,cookie,passphrase,auth,pass,pwd" {
		t.Fatalf("redacted %#v", redacted)
	}
}

func TestScrubJSONOrdinaryNamesStay(t *testing.T) {
	ordinary := []string{"command", "path", "content", "url", "query", "description", "timeout", "cwd", "session", "sessionId", "sessionName", "author", "keyboard", "key", "keys", "monkey", "objectKey", "primaryKey", "partitionKey", "cacheKey", "publicKey", "ssh_public_key", "sshPublicKey", "hostKey", "sortKey", "idempotencyKey", "tokens", "tokenizer", "num_tokens", "maxTokens", "max_tokens", "token_count", "secretary", "passage", "bypass", "cookieName", "environment", "authorName", "file_path", "old_string", "new_string", "pattern", "glob", "partition_key", "patch", "key_id", "keyName", "input_token_count", "inputTokenCount", "max_tokens_to_sample", "cached_tokens", "reasoning_tokens", "reasoningTokens", "client_public_key", "clientPublicKey", "auth_mode", "authMode", "public_keys", "sort_keys", "pass_rate", "first_pass", "pass_count", "passCount", "last_pass", "cookie_domain", "max_new_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "budget_tokens", "public_key_pem", "cache_key_v2", "key_2", "key_id_2", "authorization_url", "compass", "sha256", "s3_bucket", "pass_through", "pass_fail", "tokens_per_second", "tokensPerSecond", "output_tokens_per_second", "tokens_per_minute"}
	hidden := []string{"env", "headers", "auth", "pass", "ssh_key", "sshKey", "signing_key", "signingKey", "encryption_key", "encryptionKey", "master_key", "masterKey", "aws_key", "awsKey", "stripe_key", "stripeKey", "license_key", "licenseKey", "jwt", "pat", "bearer", "basic_auth", "basicAuth", "auth_header", "authHeader", "max_secret", "maxSecret", "secret_count", "secretCount", "APIToken", "DBPassword", "PGPassword", "JWTToken", "apiKeys", "api_keys", "password2", "db_pass", "DB_PASS", "SSHKey", "access_keys", "APIKey", "GitHubPAT", "db_passwords", "app_secrets", "auth_code", "access_tokens", "github_token", "max_token_secret", "secret_public_key", "private_public_key", "password_public_key", "read_tokens", "write_tokens", "new_tokens", "ssh_key2", "signing_key_v2", "signing_key_b64", "encryption_key_hex", "aws_key_1", "stripe_key_live", "deploy_key_pem", "signingKeyBase64", "sshKey2", "cookies", "session_cookie", "set-cookie", "Set-Cookie", "cookie_value", "pass_word", "passWord", "user_pass", "db_pass_hash", "authorization_code", "client_assertion", "admin_pass2", "DB_PASS2", "dbPass2", "db_pass_old", "db_pass_prod", "smtp_pass_live", "user_pass_v2", "mysqlPassProd", "pass_env", "pass_b64", "test_pass", "pass_2", "second_tokens", "tokens_second"}
	var buf strings.Builder
	buf.WriteByte('{')
	for i, key := range append(append([]string{}, ordinary...), hidden...) {
		if i > 0 {
			buf.WriteByte(',')
		}
		b, _ := json.Marshal(key)
		buf.Write(b)
		buf.WriteString(`:"v-`)
		buf.WriteString(key)
		buf.WriteByte('"')
	}
	buf.WriteByte('}')
	clean, redacted, err := ScrubJSON(json.RawMessage(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(clean)
	for _, key := range ordinary {
		quoted, _ := json.Marshal(key)
		if !strings.Contains(text, string(quoted)+`:"v-`+key+`"`) {
			t.Fatalf("hid %s in %s redacted %v", key, text, redacted)
		}
	}
	for _, key := range hidden {
		quoted, _ := json.Marshal(key)
		if strings.Contains(text, string(quoted)+":") {
			t.Fatalf("showed %s in %s", key, text)
		}
	}
	if strings.Join(redacted, ",") != strings.Join(hidden, ",") {
		t.Fatalf("redacted %#v", redacted)
	}
}

func TestScrubJSONBearerInsideStrings(t *testing.T) {
	cases := []struct {
		in, secret, kept string
	}{
		{`{"command":"echo 'Bearer sk-live-1' done"}`, "sk-live-1", "echo 'Bearer Hidden' done"},
		{`{"args":["-H","Bearer sk-live-2"]}`, "sk-live-2", `"Bearer Hidden"`},
		{`{"note":"bearer sk-live-3"}`, "sk-live-3", `"Bearer Hidden"`},
		{`{"nested":{"cmd":["ok","Bearer sk-live-4"]}}`, "sk-live-4", `"ok"`},
	}
	for _, tc := range cases {
		clean, _, err := ScrubJSON(json.RawMessage(tc.in))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(clean), tc.secret) || !strings.Contains(string(clean), tc.kept) {
			t.Fatalf("in %s got %s", tc.in, clean)
		}
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

func TestSecretKeyNormalizesAcronymsAndDigits(t *testing.T) {
	cases := map[string]string{
		"APIToken":   "api_token",
		"DBPassword": "db_password",
		"JWTToken":   "jwt_token",
		"SSHKey":     "ssh_key",
		"password2":  "password_2",
		"apiKeys":    "api_keys",
		"X-Api-Key":  "x_api_key",
		"GitHubPAT":  "git_hub_pat",
		"sessionId":  "session_id",
		"DB_PASS":    "db_pass",
	}
	for in, want := range cases {
		if got := normalizeSecretKey(in); got != want {
			t.Errorf("normalizeSecretKey(%q) = %q, want %q", in, got, want)
		}
	}
}
