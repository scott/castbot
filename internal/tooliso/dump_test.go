//go:build linux

package tooliso

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestNonDumpableHidesEnviron(t *testing.T) {
	if os.Getenv("CASTBOT_DUMP_CHILD") == "1" {
		if err := SetNonDumpable(); err != nil {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestNonDumpableHidesEnviron")
	cmd.Env = append(os.Environ(), "CASTBOT_DUMP_CHILD=1", "FIXTURE_ISO_SECRET=fixture-iso-value")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(3 * time.Second)
	var raw []byte
	var err error
	for time.Now().Before(deadline) {
		raw, err = os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/environ")
		if errors.Is(err, os.ErrPermission) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("environ readable or unexpected error: %v (%d bytes)", err, len(raw))
}
