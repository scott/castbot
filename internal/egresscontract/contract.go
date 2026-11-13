// Package egresscontract is the guest and host message shape for the
// connection proxy. The guest never sends a connection token. The host
// is the only side that reads one.
package egresscontract

import (
	"encoding/json"
	"strings"
)

const (
	// NotConnected is the fixed sentence a dormant proxy returns.
	NotConnected = "Connections are not available on this machine."
	// NeedsApproval is the fixed sentence when a write has no approval.
	NeedsApproval = "This action needs approval."
	// RedirectRefused is the fixed sentence when a redirect is dropped.
	RedirectRefused = "The connection proxy refused a redirect."
	// Rejected is the fixed sentence for a malformed or unsupported call.
	Rejected = "The connection request was rejected."

	// DummyCredential is the non-secret placeholder a CLI must present
	// when its own client refuses to run without a token. It is not a
	// credential. The egress strips it before the host sees the call.
	DummyCredential = "castbot-local"

	// BodyCap is the largest request or response body the guest buffers.
	BodyCap = 32 << 20
)

// SafeMessage maps a host error code onto a fixed sentence.
// Unknown codes use Rejected so a host body cannot be copied through.
func SafeMessage(code string) string {
	switch code {
	case "not_connected", "":
		return NotConnected
	case "approval_required":
		return NeedsApproval
	case "redirect_refused":
		return RedirectRefused
	default:
		return Rejected
	}
}

// KnownProvider reports whether id is a curated connection.
func KnownProvider(id string) bool {
	switch id {
	case "github", "digitalocean":
		return true
	default:
		return false
	}
}

// KnownSecretResult reports whether result is one of the four card outcomes.
func KnownSecretResult(result string) bool {
	switch result {
	case "saved", "declined", "expired", "already_configured":
		return true
	default:
		return false
	}
}

// Status is the liveness document from GET /egress and from the local
// GET /status. Enabled with an empty provider list does not rewrite
// any CLI. A missing host route is the same as enabled false.
type Status struct {
	Enabled   bool     `json:"enabled"`
	Providers []string `json:"providers"`
}

// Has reports whether provider is on an enabled status.
func (s Status) Has(provider string) bool {
	if !s.Enabled {
		return false
	}
	for _, name := range s.Providers {
		if name == provider {
			return true
		}
	}
	return false
}

// Normalize drops unknown providers and clears the list when disabled.
func (s Status) Normalize() Status {
	if !s.Enabled {
		return Status{Providers: []string{}}
	}
	var out []string
	seen := map[string]bool{}
	for _, name := range s.Providers {
		name = strings.ToLower(strings.TrimSpace(name))
		if !KnownProvider(name) || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if out == nil {
		out = []string{}
	}
	return Status{Enabled: true, Providers: out}
}

// Forward is one allowlisted HTTP or git call.
// Path is an absolute path on the provider's host. The guest does not
// choose that host. Headers must already exclude authorization.
type Forward struct {
	Provider   string            `json:"provider"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      string            `json:"query"`
	Headers    map[string]string `json:"headers"`
	BodyB64    string            `json:"body_b64"`
	ApprovalID string            `json:"approval_id"`
}

// ForwardResult is the host's upstream result.
// Status is the upstream status. A 3xx is not relayed to the guest CLI.
type ForwardResult struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
}

// Failure is the error document for a refused call.
type Failure struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// MCPCall carries one JSON-RPC message for a named shim server.
type MCPCall struct {
	Server  string          `json:"server"`
	Message json.RawMessage `json:"message"`
}

// MCPResult is the JSON-RPC message returned by the host, unchanged.
type MCPResult struct {
	Message json.RawMessage `json:"message"`
}

// SecretCall is request_secret. Name is optional and may be ignored.
type SecretCall struct {
	Connection string `json:"connection"`
	Reason     string `json:"reason"`
	Name       string `json:"name"`
}

// SecretResult is a finished card. Result is saved, declined, expired,
// or already_configured. Error is set when the call did not finish.
type SecretResult struct {
	Result  string `json:"result"`
	Error   string `json:"error"`
	Message string `json:"message"`
}
