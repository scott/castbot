// Package run drives one agent reply until the runtime sends [DONE].
// The goroutine is not tied to the browser. A cut connection reconnects
// with the data-event offset already consumed.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"time"

	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

const (
	// Unreachable is the copy shown when the runtime cannot be contacted
	// before any text arrives.
	Unreachable = "The agent is not reachable right now."
	// Stopped is the copy shown when a reply ends before [DONE].
	Stopped = "The reply stopped before it finished."

	maxStall = 8
)

var errRuntime = errors.New("runtime error")

// Job is one reply to stream.
type Job struct {
	UserID   string
	ThreadID string
	AgentID  string
	RunID    string
	Text     string
}

// Runner starts run goroutines.
type Runner struct {
	ctx    context.Context
	store  *store.Store
	client runtime.Client
	Hub    *Hub
}

// New builds a runner. Canceling ctx stops new reads. It does not by
// itself mark an in-flight run failed.
func New(ctx context.Context, st *store.Store, client runtime.Client) *Runner {
	return &Runner{ctx: ctx, store: st, client: client, Hub: &Hub{}}
}

// Start runs the job until the reply is stored or failed.
func (r *Runner) Start(job Job) {
	go r.execute(job)
}

func (r *Runner) execute(job Job) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("run %s panicked", job.RunID)
			r.fail(job, Stopped, 0)
		}
	}()
	if r.client == nil {
		r.fail(job, Unreachable, 0)
		return
	}
	if err := r.store.MarkStreaming(r.ctx, job.UserID, job.RunID); err != nil {
		log.Printf("run %s did not start", job.RunID)
		return
	}
	r.Hub.Notify(job.RunID)
	var text strings.Builder
	offset := 0
	stalls := 0
	for {
		if r.ctx.Err() != nil {
			return
		}
		stream, err := r.client.Open(r.ctx, runtime.Send{
			SessionID: job.ThreadID,
			StreamID:  job.RunID,
			Offset:    offset,
			Text:      job.Text,
		})
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			if offset == 0 {
				log.Printf("run %s: runtime unreachable", job.RunID)
				r.fail(job, Unreachable, offset)
				return
			}
			stalls++
			if stalls >= maxStall {
				r.fail(job, Stopped, offset)
				return
			}
			if !sleep(r.ctx, 10*time.Millisecond) {
				return
			}
			continue
		}
		done, got, err := r.read(stream, job, &offset, &text)
		if done {
			if err := r.finish(job, text.String(), offset); err != nil {
				log.Printf("run %s finish: %v", job.RunID, err)
				r.fail(job, Stopped, offset)
			}
			return
		}
		if r.ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRuntime) {
			r.fail(job, Stopped, offset)
			return
		}
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			log.Printf("run %s: stream cut", job.RunID)
		}
		if got == 0 {
			stalls++
			if stalls >= maxStall {
				msg := Stopped
				if offset == 0 {
					msg = Unreachable
				}
				r.fail(job, msg, offset)
				return
			}
		} else {
			stalls = 0
		}
		if !sleep(r.ctx, 10*time.Millisecond) {
			return
		}
	}
}

func (r *Runner) read(stream runtime.Stream, job Job, offset *int, text *strings.Builder) (bool, int, error) {
	defer stream.Close()
	got := 0
	for {
		frame, err := stream.Next()
		if err != nil {
			if err == io.EOF {
				return false, got, nil
			}
			return false, got, err
		}
		if strings.HasPrefix(frame.Comment, "cast-stream ") {
			continue
		}
		if !frame.DataEvent {
			continue
		}
		*offset++
		got++
		parsed, err := runtime.ParseData(frame.Data)
		if err != nil {
			return false, got, err
		}
		if parsed.Done {
			return true, got, nil
		}
		if parsed.RPCErr != "" {
			return false, got, errRuntime
		}
		if len(parsed.Perm) > 0 {
			payload, err := json.Marshal(map[string]json.RawMessage{"permission": parsed.Perm})
			if err != nil {
				return false, got, err
			}
			if _, err := r.store.AppendRunEvent(r.ctx, job.UserID, job.RunID, "permission", string(payload), *offset); err != nil {
				return false, got, err
			}
			r.Hub.Notify(job.RunID)
		}
		if parsed.Text == "" {
			continue
		}
		text.WriteString(parsed.Text)
		payload, err := json.Marshal(map[string]string{"text": parsed.Text})
		if err != nil {
			return false, got, err
		}
		if _, err := r.store.AppendRunEvent(r.ctx, job.UserID, job.RunID, "text", string(payload), *offset); err != nil {
			return false, got, err
		}
		r.Hub.Notify(job.RunID)
	}
}

func (r *Runner) finish(job Job, text string, offset int) error {
	if _, _, err := r.store.FinishRun(r.ctx, job.UserID, job.RunID, text, offset); err != nil {
		return err
	}
	r.Hub.Notify(job.RunID)
	return nil
}

func (r *Runner) fail(job Job, message string, offset int) {
	if _, err := r.store.FailRun(r.ctx, job.UserID, job.RunID, message, offset); err != nil {
		log.Printf("run %s fail: %v", job.RunID, err)
		return
	}
	r.Hub.Notify(job.RunID)
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
