package tooliso

import (
	"os"
	"testing"
	"time"
)

func TestFinishReadsClosesAHeldRead(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 8)
		_, _ = r.Read(buf)
		close(done)
	}()
	started := time.Now()
	FinishReads(done, 200*time.Millisecond, r)
	if time.Since(started) > time.Second {
		t.Fatalf("read stayed open for %s", time.Since(started))
	}
}
