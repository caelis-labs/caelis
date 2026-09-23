//go:build unix

package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"golang.org/x/sys/unix"
)

// The before-open hook makes this a deterministic Lstat→Open race, not a
// scheduler-dependent goroutine racing a filesystem mutation. No FIFO writer is
// started: a blocking open would prevent a canceled Turn from draining.
func TestApplicationArtifactFIFOReplacementDoesNotBlockDrain(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "output.txt")
	if err := os.WriteFile(path, []byte("ordinary"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	swapped := make(chan struct{})
	proceed := make(chan struct{})
	finished := make(chan error, 1)
	injectErr := make(chan error, 1)
	go func() {
		_, err := readStableWorkspaceFile(root, "output.txt", func() {
			if err := os.Rename(path, filepath.Join(workspace, "saved.txt")); err != nil {
				injectErr <- err
				close(swapped)
				return
			}
			if err := unix.Mkfifo(path, 0600); err != nil {
				injectErr <- err
				close(swapped)
				return
			}
			close(swapped)
			<-proceed
		}, nil)
		finished <- err
	}()
	<-swapped
	select {
	case err := <-injectErr:
		t.Fatalf("failed to inject FIFO: %v", err)
	default:
	}
	// The request is canceled while the worker is paused just before open.
	// Release it and require the worker to drain without a FIFO writer.
	cancel()
	close(proceed)
	deadline, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	select {
	case err := <-finished:
		if !errors.Is(err, application.ErrInvalid) {
			t.Fatalf("FIFO replacement not rejected: %v", err)
		}
	case <-deadline.Done():
		t.Fatal("FIFO replacement blocked canceled resource worker")
	}

	store := &resourceBridgeStore{}
	_, publish := testResourceBridge(t, workspace, store)
	_, err = publish.Call(ctx, testResourceCall(t, "PublishArtifact", map[string]string{"path": "output.txt", "name": "report", "media_type": "text/plain"}))
	if !errors.Is(err, application.ErrInvalid) || store.calls != 0 {
		t.Fatalf("canceled FIFO artifact reached resource Store: %v, calls=%d", err, store.calls)
	}
}
