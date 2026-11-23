package tooliso

import (
	"errors"
	"io"
	"os/exec"
	"time"
)

// ReadGrace is how long a stopped command may keep its output open
// before the read side is closed and the result is returned.
const ReadGrace = time.Second

// FinishReads waits for the output copy to finish, or for grace.
// It then closes the readers so a copy blocked on a writer that is
// still open can finish, and waits for that copy to end. Bytes already
// copied are kept.
func FinishReads(done <-chan struct{}, grace time.Duration, readers ...io.Closer) {
	timer := time.NewTimer(grace)
	select {
	case <-done:
		if !timer.Stop() {
			<-timer.C
		}
		return
	case <-timer.C:
	}
	for _, r := range readers {
		if r != nil {
			_ = r.Close()
		}
	}
	<-done
}

// WaitBounded reaps cmd. If it is still running after grace, it is
// killed and reaped so the waiter does not stay blocked.
func WaitBounded(cmd *exec.Cmd, grace time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("command")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(grace)
	select {
	case err := <-done:
		if !timer.Stop() {
			<-timer.C
		}
		return err
	case <-timer.C:
	}
	_ = cmd.Process.Kill()
	timer.Reset(grace)
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return errors.New("command")
	}
}
