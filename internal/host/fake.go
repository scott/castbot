package host

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/scott/castbot/internal/runtime"
)

// Fake is an in-memory AgentHost for tests. It does not call the machine API.
type Fake struct {
	mu         sync.Mutex
	Machines   map[string]Machine
	Creates    []CreateSpec
	Calls      []string
	Deletes    []string
	EdgeCalls  int
	CreateErr  error
	ForceState string
	Frames     []runtime.Frame
}

// NewFake returns an empty host.
func NewFake() *Fake {
	return &Fake{Machines: map[string]Machine{}}
}

// SetForce makes Status report one state. Tests use it for the machine page.
func (f *Fake) SetForce(state string) {
	f.mu.Lock()
	f.ForceState = state
	f.mu.Unlock()
}

func (f *Fake) note(call string) {
	f.mu.Lock()
	f.Calls = append(f.Calls, call)
	f.mu.Unlock()
}

func (f *Fake) edge() {
	f.mu.Lock()
	f.EdgeCalls++
	f.mu.Unlock()
}

// Create records the spec and returns a running machine.
func (f *Fake) Create(_ context.Context, spec CreateSpec) (Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "create")
	f.Creates = append(f.Creates, spec)
	if f.CreateErr != nil {
		return Machine{}, f.CreateErr
	}
	id := "vm-test"
	if len(f.Creates) > 1 {
		id = "vm-test-" + spec.AgentID
	}
	m := Machine{
		ID: id, Name: spec.Name, State: "running",
		Endpoint: "https://vm.example", Image: spec.Image, Region: spec.Region,
		CPU: spec.CPU, Memory: spec.MemoryMiB, Tags: []string{"castbot", "agent:" + spec.AgentID},
	}
	if f.ForceState != "" {
		m.State = f.ForceState
	}
	f.Machines[id] = m
	return m, nil
}

// Status returns the stored machine and does not count as an edge call.
func (f *Fake) Status(_ context.Context, id string) (Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "status")
	m, ok := f.Machines[id]
	if !ok {
		return Machine{}, ErrNotSupported
	}
	if f.ForceState != "" {
		m.State = f.ForceState
	}
	return m, nil
}

// Wake marks the machine running.
func (f *Fake) Wake(_ context.Context, id string) (Machine, error) {
	return f.set(id, "wake", "running")
}

// Pause marks the machine paused.
func (f *Fake) Pause(_ context.Context, id string) (Machine, error) {
	return f.set(id, "pause", "paused")
}

func (f *Fake) set(id, call, state string) (Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, call)
	m, ok := f.Machines[id]
	if !ok {
		return Machine{}, ErrNotSupported
	}
	m.State = state
	f.Machines[id] = m
	f.ForceState = state
	return m, nil
}

// Delete records the id and removes the machine.
func (f *Fake) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "delete")
	f.Deletes = append(f.Deletes, id)
	delete(f.Machines, id)
	return nil
}

// Probe counts an edge call and succeeds.
func (f *Fake) Probe(context.Context, string, string) error {
	f.edge()
	f.note("probe")
	return nil
}

// Stream counts an edge call and returns the scripted frames, then [DONE].
func (f *Fake) Stream(context.Context, string, string, runtime.Send) (runtime.Stream, error) {
	f.edge()
	f.mu.Lock()
	frames := append([]runtime.Frame(nil), f.Frames...)
	f.mu.Unlock()
	f.note("stream")
	frames = append(frames, runtime.Frame{Data: "[DONE]", DataEvent: true})
	return &frameStream{frames: frames}, nil
}

// Approve counts an edge call.
func (f *Fake) Approve(context.Context, string, string, string, bool) error {
	f.edge()
	f.note("approve")
	return nil
}

// Cancel does not touch the edge.
func (f *Fake) Cancel(context.Context, string, string, string) error {
	f.note("cancel")
	return ErrCancel
}

// PutFile counts an edge call.
func (f *Fake) PutFile(context.Context, string, string, string, io.Reader) error {
	f.edge()
	f.note("put")
	return nil
}

// GetFile counts an edge call.
func (f *Fake) GetFile(context.Context, string, string, string) (io.ReadCloser, error) {
	f.edge()
	f.note("get")
	return io.NopCloser(strings.NewReader("ok")), nil
}

// Reload counts an edge call.
func (f *Fake) Reload(context.Context, string, string) error {
	f.edge()
	f.note("reload")
	return nil
}

// Snapshot copies the call list.
func (f *Fake) Snapshot() (calls []string, edge int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...), f.EdgeCalls
}

type frameStream struct {
	frames []runtime.Frame
	i      int
}

func (s *frameStream) Next() (runtime.Frame, error) {
	if s.i >= len(s.frames) {
		return runtime.Frame{}, io.EOF
	}
	f := s.frames[s.i]
	s.i++
	return f, nil
}

func (s *frameStream) Close() error { return nil }
