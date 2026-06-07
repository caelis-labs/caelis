package filesystem

import (
	"os"
	"path/filepath"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/tool"
)

// testFS implements sandbox.FileSystem backed by a temp directory.
type testFS struct{ root string }

func (fs *testFS) Read(path string) ([]byte, error) { return os.ReadFile(filepath.Join(fs.root, path)) }
func (fs *testFS) Write(path string, data []byte) error {
	return os.WriteFile(filepath.Join(fs.root, path), data, 0o644)
}
func (fs *testFS) List(path string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(fs.root, path))
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}
func (fs *testFS) Exists(path string) (bool, error) {
	_, err := os.Stat(filepath.Join(fs.root, path))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
func (fs *testFS) Delete(path string) error { return os.Remove(filepath.Join(fs.root, path)) }
func (fs *testFS) Stat(path string) (sandbox.FileInfo, error) {
	info, err := os.Stat(filepath.Join(fs.root, path))
	if err != nil {
		return sandbox.FileInfo{}, err
	}
	return sandbox.FileInfo{Name: info.Name(), IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime()}, nil
}

// testContext implements tool.Context for tests.
type testContext struct{ fs sandbox.FileSystem }

func (c *testContext) Deadline() (time.Time, bool)    { return time.Time{}, false }
func (c *testContext) Done() <-chan struct{}          { return nil }
func (c *testContext) Err() error                     { return nil }
func (c *testContext) Value(_ any) any                { return nil }
func (c *testContext) SessionRef() string             { return "test-session" }
func (c *testContext) InvocationID() string           { return "test-inv" }
func (c *testContext) AgentName() string              { return "test-agent" }
func (c *testContext) FileSystem() sandbox.FileSystem { return c.fs }

func newTestContext(root string) tool.Context {
	return &testContext{fs: &testFS{root: root}}
}
