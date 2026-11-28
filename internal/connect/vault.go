package connect

import (
	"context"
	"strings"
)

type vaultKey struct{}

type vaultParts struct {
	Secret string
	Extra  string
}

// JoinVault packs a token and an optional second field into one vault value.
// The second field is separated by a tab. Neither side may contain a tab
// or a line break. An empty extra stores the token alone.
func JoinVault(secret, extra string) (string, bool) {
	if secret == "" || strings.ContainsAny(secret, "\t\n\r\x00") {
		return "", false
	}
	if extra == "" {
		return secret, true
	}
	if strings.ContainsAny(extra, "\t\n\r\x00") {
		return "", false
	}
	return secret + "\t" + extra, true
}

// SplitVault unpacks a vault value. A value with no tab has an empty extra.
// A broken packing returns ok false.
func SplitVault(value string) (secret, extra string, ok bool) {
	secret, extra, found := strings.Cut(value, "\t")
	if !found {
		if value == "" || strings.ContainsAny(value, "\n\r\x00") {
			return "", "", false
		}
		return value, "", true
	}
	if secret == "" || extra == "" || strings.ContainsAny(secret, "\t\n\r\x00") || strings.ContainsAny(extra, "\t\n\r\x00") {
		return "", "", false
	}
	return secret, extra, true
}

// VaultSecret is the token half of a vault value. A broken value is empty.
func VaultSecret(value string) string {
	secret, _, ok := SplitVault(value)
	if !ok {
		return ""
	}
	return secret
}

// WithVault attaches the secret and the extra field for a tool builder.
func WithVault(ctx context.Context, secret, extra string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, vaultKey{}, vaultParts{Secret: secret, Extra: extra})
}

// VaultFrom returns the secret and extra attached by WithVault.
func VaultFrom(ctx context.Context) (secret, extra string) {
	if ctx == nil {
		return "", ""
	}
	parts, _ := ctx.Value(vaultKey{}).(vaultParts)
	return parts.Secret, parts.Extra
}
