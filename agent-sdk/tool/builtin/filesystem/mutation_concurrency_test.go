package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Pause a Patch after it has read the target and before it can commit.
type pausedMutationFS struct {
	hostFileSystem
	read, release chan struct{}
}

func (f pausedMutationFS) ReadFile(path string) ([]byte, error) {
	data, err := f.hostFileSystem.ReadFile(path)
	close(f.read)
	<-f.release
	return data, err
}

func TestConcurrentFileMutations(t *testing.T) {
	for _, next := range []string{"patch", "guarded write", "canceled write", "hard link"} {
		t.Run(next, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			const initial = "left right"
			if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
				t.Fatal(err)
			}
			target := "file"
			if next == "hard link" {
				target = "alias"
				if err := os.Link(path, filepath.Join(dir, target)); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			}
			synctest.Test(t, func(t *testing.T) {
				host := hostFileSystem{cwd: dir}
				paused := pausedMutationFS{host, make(chan struct{}), make(chan struct{})}
				first := &PatchTool{runtime: fakeRuntime{defaultFS: paused}}
				firstDone := make(chan error, 1)
				go func() {
					_, err := first.Call(t.Context(), tool.Call{Input: json.RawMessage(`{"path":"file","edits":[{"old":"left","new":"LEFT"}]}`)})
					firstDone <- err
				}()
				<-paused.read
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				runtime := fakeRuntime{defaultFS: host}
				write := &WriteTool{runtime: runtime}
				var second tool.Tool = &PatchTool{runtime: runtime}
				input := map[string]any{"path": target, "edits": []map[string]string{{"old": "right", "new": "RIGHT"}}}
				if next == "guarded write" || next == "canceled write" {
					second = write
					input = map[string]any{"path": target, "content": "stale", "if_revision": textRevision(initial)}
				}
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					_, err := second.Call(ctx, tool.Call{Input: raw})
					done <- err
				}()
				synctest.Wait()
				if next == "canceled write" {
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Errorf("waiting Write = %v, want canceled", err)
					}
				}
				// A mutation of a different path must finish while the first is paused.
				_, err = write.Call(t.Context(), tool.Call{Input: json.RawMessage(`{"path":"other","content":"independent"}`)})
				close(paused.release)
				if err != nil {
					t.Fatal(err)
				}
				if err := <-firstDone; err != nil {
					t.Fatal(err)
				}
				want := "LEFT right"
				switch next {
				case "guarded write":
					var stale *tool.ToolError
					if err := <-done; !errors.As(err, &stale) || stale.Code != tool.ErrorCodeStaleRevision {
						t.Fatalf("guarded Write = %v, want stale revision", err)
					}
				case "patch", "hard link":
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					want = "LEFT RIGHT"
				}
				body, err := os.ReadFile(path)
				if err != nil || string(body) != want {
					t.Fatalf("file = %q, %v; want %q", body, err, want)
				}
			})
		})
	}
}
