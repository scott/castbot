// Package host starts agent machines.
// The MicroVM implementation talks to the machine API.
// The Docker helper in this package is local only and never calls that API.
package host

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/scott/castbot/internal/runtime"
)

const (
	// DefaultIdle is the auto-pause idle time when an agent has none saved.
	DefaultIdle = 5 * time.Minute
	// DefaultCPU and DefaultMemoryMiB are the only size this slice provisions.
	DefaultCPU       = 1
	DefaultMemoryMiB = 2048
	DefaultHTTPPort  = 8080
	DefaultRegion    = "mkc1"
)

var (
	// ErrFailed means the machine reached a failed state and was deleted.
	ErrFailed = errors.New("machine failed")
	// ErrName means the typed name did not match the agent.
	ErrName = errors.New("name does not match")
	// ErrCancel means the runtime has no cancel yet. The call does not
	// touch the guest, so a paused machine stays paused.
	ErrCancel = errors.New("the runtime cannot cancel a turn yet")
	// ErrNotSupported is returned for a call this host cannot make.
	ErrNotSupported = errors.New("not supported")
)

// Failed is a create that reached a failed state. The machine was deleted.
type Failed struct {
	Reason string
}

func (e *Failed) Error() string {
	if e == nil || e.Reason == "" {
		return ErrFailed.Error()
	}
	return ErrFailed.Error() + ": " + e.Reason
}

func (e *Failed) Unwrap() error { return ErrFailed }

// CreateSpec is one machine. Env on the wire is only the control plane
// URL, the agent id, and a one-time bootstrap token.
type CreateSpec struct {
	Name       string
	AgentID    string
	Image      string
	Region     string
	CPU        int
	MemoryMiB  int
	Idle       time.Duration
	HTTPPort   int
	ControlURL string
	Bootstrap  string
	// Tags are added after castbot and agent:<id>. The live harness sets castbot-live.
	Tags []string
}

// Machine is the record returned by the machine API.
// Endpoint is the edge base URL. It is empty until the API publishes a hostname.
type Machine struct {
	ID       string
	Name     string
	State    string
	Failure  string
	Endpoint string
	Image    string
	Region   string
	CPU      int
	Memory   int
	Tags     []string
}

// AgentHost is the control plane's view of one agent machine.
// Status reads the machine API only. It must not call the guest.
type AgentHost interface {
	Create(ctx context.Context, spec CreateSpec) (Machine, error)
	Status(ctx context.Context, id string) (Machine, error)
	Wake(ctx context.Context, id string) (Machine, error)
	Pause(ctx context.Context, id string) (Machine, error)
	Delete(ctx context.Context, id string) error
	Probe(ctx context.Context, endpoint, agentToken string) error
	Stream(ctx context.Context, endpoint, agentToken string, req runtime.Send) (runtime.Stream, error)
	Approve(ctx context.Context, endpoint, agentToken, requestID string, allow bool) error
	Cancel(ctx context.Context, endpoint, agentToken, streamID string) error
	PutFile(ctx context.Context, endpoint, agentToken, name string, body io.Reader) error
	GetFile(ctx context.Context, endpoint, agentToken, name string) (io.ReadCloser, error)
	Reload(ctx context.Context, endpoint, agentToken string) error
}

// ConfirmName accepts a delete only when the typed text is the agent name.
func ConfirmName(typed, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.TrimSpace(typed) != name {
		return ErrName
	}
	return nil
}

// MachineName is the API name for one agent. It is lowercase and fits the
// 63-character name limit.
func MachineName(agentID string) string {
	name := "castbot-" + strings.ToLower(strings.TrimSpace(agentID))
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

// LiveLeftovers returns castbot-live machines this process may delete.
// Demo names and the castbot-s00 tag stay out even if they also carry the tag.
func LiveLeftovers(vms []Machine) []Machine {
	var out []Machine
	for _, vm := range vms {
		tagged := false
		for _, tag := range vm.Tags {
			if tag == "castbot-live" {
				tagged = true
			}
		}
		if !tagged {
			continue
		}
		if err := GuardDelete(vm); err != nil {
			continue
		}
		out = append(out, vm)
	}
	return out
}

// LiveWindow reports whether a live test has enough time to create a machine
// and still delete it if the run is slow. go test's default timeout is 10
// minutes, which is not enough, and a timeout panic skips cleanup.
func LiveWindow(deadline time.Time, hasDeadline bool, now time.Time) error {
	if !hasDeadline || deadline.Sub(now) < 40*time.Minute {
		return errors.New("live test needs go test -tags live -timeout 45m ./internal/host")
	}
	return nil
}

// GuardDelete refuses the two demo machines and the leftover probe tag.
// The live harness calls it before a delete. Product deletes of machines
// this process created do not need it, because those rows are not the demos.
func GuardDelete(m Machine) error {
	if strings.HasPrefix(m.Name, "cast-demo-agent") {
		return errors.New("refusing to delete a demo machine")
	}
	for _, tag := range m.Tags {
		if tag == "castbot-s00" {
			return errors.New("refusing to delete a probe machine")
		}
	}
	return nil
}
