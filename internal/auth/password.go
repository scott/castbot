// Package auth hashes passwords and checks authenticator codes.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the Argon2id settings stored with each hash.
type Params struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint32
}

// DefaultParams is what the server uses for new passwords.
var DefaultParams = Params{Time: 1, Memory: 64 * 1024, Threads: 1, KeyLen: 32}

// Hash returns an Argon2id encoding of password.
func Hash(password string, p Params) (string, error) {
	if p.KeyLen == 0 {
		p.KeyLen = 32
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	), nil
}

// Verify reports whether password matches an encoding from Hash.
func Verify(password, encoded string) bool {
	p, salt, sum, err := parse(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(sum)))
	return subtle.ConstantTimeCompare(got, sum) == 1
}

func parse(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return Params{}, nil, nil, errors.New("bad hash")
	}
	var p Params
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return Params{}, nil, nil, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return Params{}, nil, nil, err
	}
	sum, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, err
	}
	if len(salt) < 8 || len(sum) < 16 {
		return Params{}, nil, nil, errors.New("bad hash")
	}
	return p, salt, sum, nil
}
