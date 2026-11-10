package run

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/scott/castbot/internal/runtime"
)

func TestTransientIncludesResetAndTimeouts(t *testing.T) {
	reset := &url.Error{Op: "read", Err: &net.OpError{Op: "read", Err: syscall.ECONNRESET}}
	refused := &url.Error{Op: "dial", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}
	dial := &url.Error{Op: "dial", Err: timeoutErr("dial timeout")}
	header := &url.Error{Op: "Get", Err: timeoutErr("timeout awaiting response headers")}
	deadline := &url.Error{Op: "dial", Err: context.DeadlineExceeded}
	closed := &url.Error{Op: "Get", Err: io.EOF}
	cut := &url.Error{Op: "Get", Err: io.ErrUnexpectedEOF}
	for _, err := range []error{reset, refused, dial, header, deadline, closed, cut, &runtime.StatusError{Code: 502}} {
		if !transient(err) {
			t.Fatalf("want transient: %v", err)
		}
	}
	if transient(errors.New("no such host")) || transient(context.Canceled) || transient(&runtime.StatusError{Code: 400}) {
		t.Fatal("classified a hard failure as transient")
	}
}

type timeoutErr string

func (e timeoutErr) Error() string   { return string(e) }
func (e timeoutErr) Timeout() bool   { return true }
func (e timeoutErr) Temporary() bool { return true }
