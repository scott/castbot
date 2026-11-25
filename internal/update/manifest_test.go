package update

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"aead.dev/minisign"
)

func TestSignRejectsAFlippedByte(t *testing.T) {
	pub, privText := testKey(t)
	msg := []byte("castbot release\n")
	sig, err := Sign(privText, msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(pub, msg, sig); err != nil {
		t.Fatal(err)
	}
	flipped := append([]byte(nil), msg...)
	flipped[0] ^= 0xff
	if err := Verify(pub, flipped, sig); err == nil {
		t.Fatal("flipped message verified")
	}
	badSig := append([]byte(nil), sig...)
	badSig[len(badSig)-2] ^= 0xff
	if err := Verify(pub, msg, badSig); err == nil {
		t.Fatal("flipped signature verified")
	}
}

func TestPackAndVerify(t *testing.T) {
	pub, privText := testKey(t)
	dir := t.TempDir()
	amd := filepath.Join(dir, "amd")
	arm := filepath.Join(dir, "arm")
	if err := os.WriteFile(amd, []byte("amd64-binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(arm, []byte("arm64-binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit := "0123456789abcdef0123456789abcdef01234567"
	in := Input{
		Version:            "0.20261124.1",
		Commit:             commit,
		Schema:             37,
		GuestID:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RuntimeImage:       "ghcr.io/scott/castbot-runtime:" + commit,
		RuntimeImageCommit: commit,
		Notes:              "https://github.com/scott/castbot/releases/tag/v0.20261124.1",
		AMD64:              amd,
		ARM64:              arm,
	}
	out := filepath.Join(dir, "out")
	if err := Pack(out, in, privText); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDir(out, pub); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got Manifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Manifest != 1 || got.Version != in.Version || got.Schema != 37 || got.Assets["linux-amd64"].Size != int64(len("amd64-binary")) {
		t.Fatalf("manifest %+v", got)
	}
	bin := filepath.Join(out, "castbot_linux_amd64")
	body, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	body[0] ^= 0xff
	if err := os.WriteFile(bin, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDir(out, pub); err == nil {
		t.Fatal("flipped binary verified")
	}
}

func TestEmbeddedPublicKeyParses(t *testing.T) {
	var key minisign.PublicKey
	if err := key.UnmarshalText(PublicKey()); err != nil {
		t.Fatal(err)
	}
}

// TestWriteInstallFixture packs a signed release when CASTBOT_FIXTURE_DIR
// is set. scripts/install_test.sh uses it. A normal test run does nothing.
func TestWriteInstallFixture(t *testing.T) {
	dir := os.Getenv("CASTBOT_FIXTURE_DIR")
	if dir == "" {
		return
	}
	stub := filepath.Join(dir, "stub")
	pub, privText := testKey(t)
	if err := os.WriteFile(filepath.Join(dir, "key.pub"), pub, 0o644); err != nil {
		t.Fatal(err)
	}
	commit := "fedcba9876543210fedcba9876543210fedcba98"
	guest := "abababababababababababababababababababababababababababababababab"
	err := Pack(filepath.Join(dir, "out"), Input{
		Version:            "0.20261124.1",
		Commit:             commit,
		Schema:             37,
		GuestID:            guest,
		RuntimeImage:       "ghcr.io/scott/castbot-runtime:" + commit,
		RuntimeImageCommit: commit,
		Notes:              "https://github.com/scott/castbot/releases/tag/v0.20261124.1",
		AMD64:              stub,
		ARM64:              stub,
	}, privText)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRejectsBadVersion(t *testing.T) {
	in := Input{Version: "dev", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := in.Validate(); err == nil {
		t.Fatal("dev version was accepted")
	}
}

func testKey(t *testing.T) (public, private []byte) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err = pub.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	private, err = priv.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(private, []byte("BEGIN")) {
		t.Fatal("private key format changed")
	}
	return public, private
}
