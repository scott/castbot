package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// NewTOTPSecret returns a base32 secret for an authenticator app.
func NewTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(buf), "="), nil
}

// OTPAuthURL is the enrollment URI for secret.
func OTPAuthURL(email, secret string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", "castbot")
	v.Set("period", "30")
	v.Set("digits", "6")
	return "otpauth://totp/" + url.PathEscape("castbot:"+email) + "?" + v.Encode()
}

// TOTPCode is the 6-digit code for secret at t.
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	counter := uint64(t.Unix() / 30)
	return hotp(key, counter), nil
}

// TOTPVerify accepts the current code and one step on either side.
func TOTPVerify(secret, code string, t time.Time) bool {
	_, ok := TOTPMatch(secret, code, t)
	return ok
}

// TOTPMatch returns the newest 30 second step that matches code.
// It accepts the current step and one step on either side.
func TOTPMatch(secret, code string, t time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false
	}
	counter := t.Unix() / 30
	var matched int64
	ok := false
	for _, delta := range []int64{-1, 0, 1} {
		step := counter + delta
		if step < 0 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hotp(key, uint64(step))), []byte(code)) == 1 {
			if !ok || step > matched {
				matched = step
				ok = true
			}
		}
	}
	return matched, ok
}

func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.TrimSpace(secret))
	if m := len(s) % 8; m != 0 {
		s += strings.Repeat("=", 8-m)
	}
	return base32.StdEncoding.DecodeString(s)
}

func hotp(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", bin%1000000)
}
