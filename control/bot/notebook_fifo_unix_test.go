//go:build unix

package bot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// A FIFO is the one special file a plain open can block on. The notebook opens
// with O_NONBLOCK and rejects the opened type, so Read and Grep must fail
// promptly instead of waiting for a writer.
func TestNotebookRejectsFIFOWithoutBlocking(t *testing.T) {
	notebook := newTestNotebook(t, t.TempDir())
	mustInitNotebook(t, notebook)
	if err := syscall.Mkfifo(filepath.Join(notebookBase(t, notebook), "blocked.fifo"), 0o600); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}
	tools := notebookToolSet(t, notebook)

	for _, name := range []string{"Read", "Grep"} {
		args := map[string]any{"path": "blocked.fifo"}
		if name == "Grep" {
			args["pattern"] = "needle"
		}
		input, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("Marshal(%s) error = %v", name, err)
		}
		done := make(chan error, 1)
		go func() {
			_, callErr := tools[name].Call(context.Background(), tool.Call{Input: input})
			done <- callErr
		}()
		select {
		case callErr := <-done:
			if callErr == nil || !strings.Contains(callErr.Error(), "not a regular file") {
				t.Fatalf("%s(fifo) error = %v, want regular-file error", name, callErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s(fifo) blocked on a special file", name)
		}
	}

	input, err := json.Marshal(map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatalf("Marshal(walk) error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, callErr := tools["Grep"].Call(context.Background(), tool.Call{Input: input})
		done <- callErr
	}()
	select {
	case callErr := <-done:
		if callErr != nil {
			t.Fatalf("Grep(walk with fifo) error = %v", callErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Grep(walk) blocked on a special file")
	}
}
