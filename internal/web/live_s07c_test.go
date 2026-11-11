//go:build live

package web

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/replace"
)

// TestLiveReplaceMachine copies memory and one workspace file onto a new
// throwaway machine, then deletes the old one. It does not run unless
// CASTBOT_DO_TOKEN, CASTBOT_IMAGE, and an https CASTBOT_PUBLIC_URL are set.
// The name stays inside the existing castbot-s07-test- prefix so the live
// harness will clean it up. Do not point this at a machine that is in use.
//
//	CASTBOT_DO_TOKEN=... CASTBOT_IMAGE=... CASTBOT_PUBLIC_URL=https://... \
//	  go test -tags live -timeout 45m -count=1 -run TestLiveReplaceMachine ./internal/web
func TestLiveReplaceMachine(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CASTBOT_DO_TOKEN")) == "" {
		t.Skip("CASTBOT_DO_TOKEN is not set")
	}
	image := strings.TrimSpace(os.Getenv("CASTBOT_IMAGE"))
	publicURL := strings.TrimRight(strings.TrimSpace(os.Getenv("CASTBOT_PUBLIC_URL")), "/")
	rig := s07Live(t, "castbot-s07-test-s07c", 0)
	endpoint, token := rig.waitReady(t)
	ctx := context.Background()
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	body := []byte("remember")
	if err := tw.WriteHeader(&tar.Header{Name: "note.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rig.client.ImportMemory(ctx, endpoint, token, bytes.NewReader(tarBuf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if err := rig.client.WriteWorkspace(ctx, endpoint, token, "uploads/hello.txt", strings.NewReader("hello file")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		listed, err := rig.client.List(context.Background())
		if err != nil {
			t.Errorf("list: %v", err)
			return
		}
		for _, vm := range listed {
			if !strings.Contains(vm.Name, rig.agent.ID) {
				continue
			}
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = rig.client.Delete(cctx, vm.ID)
			cancel()
		}
	})
	res, err := replace.Run(ctx, rig.h.st, rig.client, replace.Options{
		UserID: rig.agent.UserID, AgentID: rig.agent.ID, ActorKind: "cli", ActorID: "cli", Actor: "cli",
		Image: image, PublicURL: publicURL, DataDir: t.TempDir(), BootWait: 4 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 {
		t.Fatalf("files %d", res.Files)
	}
	live, err := rig.h.st.LiveVM(ctx, rig.agent.UserID, rig.agent.ID)
	if err != nil || live.MicroVMID == "" || live.MicroVMID == rig.created() {
		t.Fatalf("live %+v %v", live, err)
	}
	endpoint, token, err = rig.h.st.VMCredentials(ctx, rig.agent.UserID, rig.agent.ID)
	if err != nil || token == "" {
		t.Fatal(err)
	}
	got, err := rig.client.ReadWorkspace(ctx, endpoint, token, "uploads/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	raw, err := io.ReadAll(got)
	if err != nil || string(raw) != "hello file" {
		t.Fatalf("file %q %v", raw, err)
	}
}
