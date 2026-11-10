package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	p := Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}
	enc, err := Hash("correct horse battery", p)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify("correct horse battery", enc) {
		t.Fatal("expected match")
	}
	if Verify("wrong horse battery", enc) {
		t.Fatal("expected mismatch")
	}
	if Verify("correct horse battery", "nope") {
		t.Fatal("expected bad encoding to fail")
	}
}

func TestTOTPWindow(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 15, 4, 5, 0, time.UTC)
	code, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if !TOTPVerify(secret, code, now.Add(20*time.Second)) {
		t.Fatal("expected the current code to pass inside the window")
	}
	if code != "000000" && TOTPVerify(secret, "000000", now) {
		t.Fatal("wrong code was accepted")
	}
	if !strings.Contains(OTPAuthURL("a@example.com", secret), "issuer=castbot") {
		t.Fatal("otpauth issuer")
	}
}
