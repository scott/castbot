//go:build linux

package tooliso

import (
	"os"
	"testing"
)

func TestSupervisorRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("this test must not run as root")
	}
	if _, err := StartRuntimeHost([]string{"/bin/true"}); err == nil {
		t.Fatal("started a privileged process")
	}
}
