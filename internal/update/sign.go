package update

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"aead.dev/minisign"
)

//go:embed minisign.pub
var publicKeyText []byte

// PublicKey is the release signing key compiled into the binary.
func PublicKey() []byte {
	return append([]byte(nil), publicKeyText...)
}

// Sign returns a minisign signature over message.
// privateKeyText is the minisign private key file, unencrypted.
func Sign(privateKeyText, message []byte) ([]byte, error) {
	var key minisign.PrivateKey
	if err := key.UnmarshalText(privateKeyText); err != nil {
		return nil, err
	}
	if len(message) == 0 {
		return nil, errors.New("message is empty")
	}
	return minisign.Sign(key, message), nil
}

// Verify reports whether signature covers message under the public key file.
func Verify(publicKey, message, signature []byte) error {
	var key minisign.PublicKey
	if err := key.UnmarshalText(publicKey); err != nil {
		return err
	}
	if !minisign.Verify(key, message, signature) {
		return errors.New("signature rejected")
	}
	return nil
}

// Pack writes release.json, SHA256SUMS, and SHA256SUMS.minisig into dir.
func Pack(dir string, in Input, privateKey []byte) error {
	m, err := Build(in)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	manifestPath := filepath.Join(dir, "release.json")
	if err := WriteManifest(manifestPath, m); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(dir, "castbot_linux_amd64"), in.AMD64); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(dir, "castbot_linux_arm64"), in.ARM64); err != nil {
		return err
	}
	sums, err := EncodeSums(dir, []string{"release.json", "castbot_linux_amd64", "castbot_linux_arm64"})
	if err != nil {
		return err
	}
	if err := writeNew(filepath.Join(dir, "SHA256SUMS"), sums); err != nil {
		return err
	}
	sig, err := Sign(privateKey, sums)
	if err != nil {
		return err
	}
	return writeNew(filepath.Join(dir, "SHA256SUMS.minisig"), sig)
}

// VerifyDir checks SHA256SUMS.minisig and every file the sums name.
func VerifyDir(dir string, publicKey []byte) error {
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS.minisig"))
	if err != nil {
		return err
	}
	if err := Verify(publicKey, sums, sig); err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(sums), "\n"), "\n")
	if len(lines) == 0 {
		return errors.New("sums are empty")
	}
	for _, line := range lines {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || !sumRE.MatchString(sum) || !fileNameOK(name) {
			return errors.New("sum line is unexpected")
		}
		asset, err := HashFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if asset.SHA256 != sum {
			return errors.New("sum does not match")
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil {
		return err
	}
	if !bytes.Contains(sums, []byte("  release.json\n")) {
		return errors.New("release.json is not in the sums")
	}
	if len(raw) == 0 {
		return errors.New("release.json is empty")
	}
	return nil
}

func copyFile(dst, src string) error {
	if filepath.Clean(dst) == filepath.Clean(src) {
		return nil
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeNew(dst, raw)
}
