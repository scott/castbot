package web

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
)

func TestImageRootBoundary(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if err := host.BuildRuntime(ctx, root); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "boundary")
	build := exec.Command("go", "test", "-c", "-o", bin, "./internal/castlet")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %v\n%s", err, out)
	}
	cmd := exec.Command("docker", "run", "--rm",
		"--security-opt", "no-new-privileges",
		"--user", "root",
		"-e", "CASTBOT_BOUNDARY=1",
		"-v", bin+":/boundary:ro",
		"--entrypoint", "/boundary",
		host.RuntimeImage,
		"-test.run", "^TestAppBoundary$", "-test.v")
	out, err := cmd.CombinedOutput()
	if err != nil || bytes.Contains(out, []byte("FAIL")) || !bytes.Contains(out, []byte("PASS")) {
		t.Fatalf("boundary %v\n%s", err, out)
	}
}
