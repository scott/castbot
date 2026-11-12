package host

import (
	"archive/tar"
	"bytes"
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
	// WaitCreate blocks Create until it is closed or the context ends.
	WaitCreate <-chan struct{}
	// CreateStarted is signaled when Create begins. Tests use a buffer of 1.
	CreateStarted chan struct{}
	// BlankEndpoint makes Create return a running machine with no address.
	BlankEndpoint bool
	// RejectAddress means a published hostname failed the edge check.
	// An empty address without this flag is still unpublished.
	RejectAddress bool
	// ReloadWait blocks Reload until it is closed or the context ends.
	ReloadWait <-chan struct{}
	// ReloadStarted is signaled when Reload begins. Tests use a buffer of 1.
	ReloadStarted chan struct{}
	// ReloadErr is returned after ReloadWait, when one is set.
	ReloadErr error
	// Pushed records CLI token hash bodies.
	Pushed [][]byte
	// Journal is the body PullJournal returns. An empty value is an empty entry list.
	Journal []byte
	// ListErr makes List fail. The reconciler backs off on it.
	ListErr error
	// WakeWait blocks Wake until it is closed or the context ends.
	WakeWait <-chan struct{}
	// Approvals records permission/respond calls.
	Approvals []ApprovalCall
	// ApproveErr is returned from Approve after the call is recorded.
	ApproveErr error
	// OnCreate runs after a machine is stored. Tests exchange the bootstrap here.
	// The spec carries the bootstrap token. Do not log it.
	OnCreate func(CreateSpec)
	// FailClass makes one replace step fail. create, boot, read_old, copy, verify.
	FailClass string
	// DeleteErr makes the next Delete fail and is then cleared.
	DeleteErr error
	// HealthVersion is the bundle version Health returns.
	HealthVersion string
	// Disks holds memory tars and workspace files keyed by endpoint.
	Disks map[string]*GuestDisk
	// createdEndpoint is the address of the latest Create.
	createdEndpoint string
	healthCalls     map[string]int
}

// GuestDisk is one fake guest's memory tar and workspace files.
type GuestDisk struct {
	Memory   []byte
	Files    map[string][]byte
	Reloaded bool
}

// ApprovalCall is one permission/respond the fake observed.
type ApprovalCall struct {
	RequestID string
	Allow     bool
}

// NewFake returns an empty host.
func NewFake() *Fake {
	return &Fake{Machines: map[string]Machine{}}
}

// SetForce makes Status report one state. Tests use it for the machine page.
// SetReloadWait holds Reload until the channel is closed. Nil releases the next call.
func (f *Fake) SetReloadWait(wait <-chan struct{}) {
	f.mu.Lock()
	f.ReloadWait = wait
	f.mu.Unlock()
}

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

// resume models the edge: a request to a paused machine starts it again.
func (f *Fake) resume(endpoint string) {
	f.mu.Lock()
	f.resumeLocked(endpoint)
	f.mu.Unlock()
}

func (f *Fake) resumeLocked(endpoint string) {
	if endpoint == "" {
		return
	}
	for id, m := range f.Machines {
		if m.Endpoint == endpoint && m.State == "paused" {
			m.State = "running"
			f.Machines[id] = m
		}
	}
}

// Create records the spec and returns a running machine.
func (f *Fake) Create(ctx context.Context, spec CreateSpec) (Machine, error) {
	f.mu.Lock()
	wait := f.WaitCreate
	started := f.CreateStarted
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		case <-ctx.Done():
			return Machine{}, ctx.Err()
		}
	}
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return Machine{}, ctx.Err()
		}
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, "create")
	f.Creates = append(f.Creates, spec)
	if f.CreateErr != nil {
		err := f.CreateErr
		f.mu.Unlock()
		return Machine{}, err
	}
	id := "vm-test"
	if len(f.Creates) > 1 {
		id = "vm-test-" + spec.AgentID
	}
	if f.FailClass == "create" {
		f.mu.Unlock()
		return Machine{}, ErrGuest
	}
	endpoint := "https://vm.example"
	if f.BlankEndpoint || f.RejectAddress {
		endpoint = ""
	} else {
		for _, existing := range f.Machines {
			if existing.Endpoint == endpoint {
				endpoint = "https://" + id + ".example"
				break
			}
		}
	}
	tags := []string{"castbot", "agent:" + spec.AgentID}
	seen := map[string]bool{"castbot": true, "agent:" + spec.AgentID: true}
	for _, tag := range spec.Tags {
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	m := Machine{
		ID: id, Name: spec.Name, State: "running",
		Endpoint: endpoint, AddressRejected: f.RejectAddress, Image: spec.Image, Region: spec.Region,
		CPU: spec.CPU, Memory: spec.MemoryMiB, Tags: tags,
	}
	if f.ForceState != "" {
		m.State = f.ForceState
	}
	f.Machines[id] = m
	f.createdEndpoint = endpoint
	if f.Disks == nil {
		f.Disks = map[string]*GuestDisk{}
	}
	if _, ok := f.Disks[endpoint]; !ok && endpoint != "" {
		f.Disks[endpoint] = &GuestDisk{Files: map[string][]byte{}}
	}
	accepted := spec.Accepted
	onCreate := f.OnCreate
	f.mu.Unlock()
	if accepted != nil {
		accepted(Machine{ID: m.ID, Name: m.Name, State: "new"})
	}
	if onCreate != nil {
		onCreate(spec)
	}
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
func (f *Fake) Wake(ctx context.Context, id string) (Machine, error) {
	f.mu.Lock()
	wait := f.WakeWait
	f.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return Machine{}, ctx.Err()
		}
	}
	return f.set(id, "wake", "running")
}

// List returns the stored machines. It does not call a guest.
func (f *Fake) List(context.Context) ([]Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "list")
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	out := make([]Machine, 0, len(f.Machines))
	for _, m := range f.Machines {
		if f.ForceState != "" {
			m.State = f.ForceState
		}
		out = append(out, m)
	}
	return out, nil
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
	if f.DeleteErr != nil {
		err := f.DeleteErr
		f.DeleteErr = nil
		return err
	}
	f.Deletes = append(f.Deletes, id)
	delete(f.Machines, id)
	return nil
}

// SeedDisk stores a memory tar and workspace files for one endpoint.
func (f *Fake) SeedDisk(endpoint string, memory []byte, files map[string][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Disks == nil {
		f.Disks = map[string]*GuestDisk{}
	}
	cp := map[string][]byte{}
	for name, body := range files {
		cp[name] = append([]byte(nil), body...)
	}
	f.Disks[endpoint] = &GuestDisk{Memory: append([]byte(nil), memory...), Files: cp}
}

// Probe counts an edge call and succeeds.
func (f *Fake) Probe(_ context.Context, endpoint, _ string) error {
	f.resume(endpoint)
	f.edge()
	f.note("probe")
	return nil
}

// Stream counts an edge call and returns the scripted frames, then [DONE].
func (f *Fake) Stream(_ context.Context, endpoint, _ string, _ runtime.Send) (runtime.Stream, error) {
	f.resume(endpoint)
	f.edge()
	f.mu.Lock()
	frames := append([]runtime.Frame(nil), f.Frames...)
	f.mu.Unlock()
	f.note("stream")
	frames = append(frames, runtime.Frame{Data: "[DONE]", DataEvent: true})
	return &frameStream{frames: frames}, nil
}

// Decisions returns recorded permission/respond calls.
func (f *Fake) Decisions() (calls []string, approvals []ApprovalCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...), append([]ApprovalCall(nil), f.Approvals...)
}

// Approve counts an edge call and records the decision.
func (f *Fake) Approve(_ context.Context, endpoint, _, requestID string, allow bool) error {
	f.resume(endpoint)
	f.edge()
	f.mu.Lock()
	f.Calls = append(f.Calls, "approve")
	f.Approvals = append(f.Approvals, ApprovalCall{RequestID: requestID, Allow: allow})
	err := f.ApproveErr
	f.mu.Unlock()
	return err
}

// Cancel does not touch the edge.
func (f *Fake) Cancel(context.Context, string, string, string) error {
	f.note("cancel")
	return ErrCancel
}

// PutFile counts an edge call.
func (f *Fake) PutFile(_ context.Context, endpoint, _, _ string, _ io.Reader) error {
	f.resume(endpoint)
	f.edge()
	f.note("put")
	return nil
}

// GetFile counts an edge call.
func (f *Fake) GetFile(_ context.Context, endpoint, _, _ string) (io.ReadCloser, error) {
	f.resume(endpoint)
	f.edge()
	f.note("get")
	return io.NopCloser(strings.NewReader("ok")), nil
}

// Reload counts an edge call. A test can hold it in ReloadWait.
func (f *Fake) Reload(ctx context.Context, endpoint, _ string) error {
	f.mu.Lock()
	wait := f.ReloadWait
	started := f.ReloadStarted
	reloadErr := f.ReloadErr
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.resume(endpoint)
	f.edge()
	f.note("reload")
	f.mu.Lock()
	if d := f.Disks[endpoint]; d != nil {
		d.Reloaded = reloadErr == nil
	}
	f.mu.Unlock()
	return reloadErr
}

// PushCLI records a token-hash push. It counts as an edge call.
func (f *Fake) PushCLI(_ context.Context, endpoint, _ string, body []byte) error {
	f.resume(endpoint)
	f.edge()
	f.mu.Lock()
	f.Pushed = append(f.Pushed, append([]byte(nil), body...))
	f.mu.Unlock()
	f.note("push-cli")
	return nil
}

// PullJournal returns the scripted journal. It counts as an edge call.
func (f *Fake) PullJournal(_ context.Context, endpoint, _ string, _ int) ([]byte, error) {
	f.resume(endpoint)
	f.edge()
	f.mu.Lock()
	body := append([]byte(nil), f.Journal...)
	f.mu.Unlock()
	f.note("pull-journal")
	if len(body) == 0 {
		body = []byte(`{"entries":[]}`)
	}
	return body, nil
}

// Health returns the scripted bundle version.
func (f *Fake) Health(_ context.Context, endpoint, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeLocked(endpoint)
	f.Calls = append(f.Calls, "health")
	if f.healthCalls == nil {
		f.healthCalls = map[string]int{}
	}
	n := f.healthCalls[endpoint]
	f.healthCalls[endpoint] = n + 1
	if f.FailClass == "boot" && endpoint == f.createdEndpoint && n == 0 {
		return "", ErrGuest
	}
	if f.FailClass == "verify" && endpoint == f.createdEndpoint && n > 0 {
		return "", ErrGuest
	}
	return f.HealthVersion, nil
}

// ExportMemory returns the seeded memory tar.
func (f *Fake) ExportMemory(_ context.Context, endpoint, _ string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeLocked(endpoint)
	f.Calls = append(f.Calls, "export-memory")
	if f.FailClass == "read_old" && endpoint != f.createdEndpoint {
		return nil, ErrGuest
	}
	d := f.diskLocked(endpoint)
	body := append([]byte(nil), d.Memory...)
	if len(body) == 0 {
		body = emptyTar()
	}
	return io.NopCloser(strings.NewReader(string(body))), nil
}

// ImportMemory stores a memory tar on the endpoint.
func (f *Fake) ImportMemory(_ context.Context, endpoint, _ string, body io.Reader) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeLocked(endpoint)
	f.Calls = append(f.Calls, "import-memory")
	if f.FailClass == "copy" {
		return ErrGuest
	}
	d := f.diskLocked(endpoint)
	d.Memory = raw
	return nil
}

// ListWorkspace returns the seeded file names and sizes.
func (f *Fake) ListWorkspace(_ context.Context, endpoint, _ string) ([]WorkspaceFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeLocked(endpoint)
	f.Calls = append(f.Calls, "list-files")
	if f.FailClass == "read_old" && endpoint != f.createdEndpoint {
		return nil, ErrGuest
	}
	d := f.diskLocked(endpoint)
	out := make([]WorkspaceFile, 0, len(d.Files))
	for name, body := range d.Files {
		out = append(out, WorkspaceFile{Name: name, Size: int64(len(body))})
	}
	return out, nil
}

// ReadWorkspace returns one seeded file.
func (f *Fake) ReadWorkspace(_ context.Context, endpoint, _, name string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeLocked(endpoint)
	f.Calls = append(f.Calls, "read-file")
	if f.FailClass == "read_old" && endpoint != f.createdEndpoint {
		return nil, ErrGuest
	}
	d := f.diskLocked(endpoint)
	body, ok := d.Files[name]
	if !ok {
		return nil, ErrNotSupported
	}
	return io.NopCloser(strings.NewReader(string(body))), nil
}

// WriteWorkspace stores one file.
func (f *Fake) WriteWorkspace(_ context.Context, endpoint, _, name string, body io.Reader) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeLocked(endpoint)
	f.Calls = append(f.Calls, "write-file")
	if f.FailClass == "copy" {
		return ErrGuest
	}
	d := f.diskLocked(endpoint)
	if d.Files == nil {
		d.Files = map[string][]byte{}
	}
	d.Files[name] = raw
	return nil
}

func (f *Fake) diskLocked(endpoint string) *GuestDisk {
	if f.Disks == nil {
		f.Disks = map[string]*GuestDisk{}
	}
	d, ok := f.Disks[endpoint]
	if !ok {
		d = &GuestDisk{Files: map[string][]byte{}}
		f.Disks[endpoint] = d
	}
	if d.Files == nil {
		d.Files = map[string][]byte{}
	}
	return d
}

func emptyTar() []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.Close()
	return buf.Bytes()
}

// SetListErr makes the next List calls fail. A nil error clears it.
func (f *Fake) SetListErr(err error) {
	f.mu.Lock()
	f.ListErr = err
	f.mu.Unlock()
}

// SetMachineState updates one stored machine and clears a forced state.
// The reconciler reads this from List. It does not call the guest.
func (f *Fake) SetMachineState(id, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.Machines[id]
	m.ID = id
	m.State = state
	f.Machines[id] = m
	f.ForceState = ""
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
