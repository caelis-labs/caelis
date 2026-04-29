package filesystem

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
)

func TestFileSystemFromRuntimeUsesConstraintAwareSelector(t *testing.T) {
	defaultFS := fakeFileSystem{cwd: "/sandbox"}
	hostFS := fakeFileSystem{cwd: "/host"}
	runtime := fakeRuntime{
		defaultFS: defaultFS,
		hostFS:    hostFS,
	}

	got := fileSystemFromRuntime(runtime, map[string]any{
		"sandbox_constraints": sdksandbox.Constraints{
			Route:      sdksandbox.RouteHost,
			Permission: sdksandbox.PermissionFullAccess,
		},
	})
	if got == nil {
		t.Fatal("fileSystemFromRuntime() = nil")
	}
	cwd, err := got.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if cwd != "/host" {
		t.Fatalf("Getwd() = %q, want /host", cwd)
	}
}

func TestFileSystemFromRuntimeFallsBackToDefaultRuntimeFS(t *testing.T) {
	defaultFS := fakeFileSystem{cwd: "/sandbox"}
	runtime := fakeRuntime{defaultFS: defaultFS}

	got := fileSystemFromRuntime(runtime, nil)
	if got == nil {
		t.Fatal("fileSystemFromRuntime() = nil")
	}
	cwd, err := got.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if cwd != "/sandbox" {
		t.Fatalf("Getwd() = %q, want /sandbox", cwd)
	}
}

func TestGitignoreExcludePatternsPreserveAnchoredRules(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/vendor\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.gitignore) error = %v", err)
	}
	patterns := gitignoreExcludePatterns(hostFileSystem{cwd: root}, root)

	if !shouldExcludePath(root, filepath.Join(root, "vendor"), true, patterns) {
		t.Fatal("root vendor path was not excluded")
	}
	if shouldExcludePath(root, filepath.Join(root, "sub", "vendor"), true, patterns) {
		t.Fatal("nested vendor path was excluded by anchored /vendor rule")
	}
}

func TestGitignoreExcludePatternsHonorNegatedRules(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n!important.log\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.gitignore) error = %v", err)
	}
	patterns := gitignoreExcludePatterns(hostFileSystem{cwd: root}, root)

	if !shouldExcludePath(root, filepath.Join(root, "debug.log"), false, patterns) {
		t.Fatal("ordinary log file was not excluded")
	}
	if shouldExcludePath(root, filepath.Join(root, "important.log"), false, patterns) {
		t.Fatal("negated important.log rule was not honored")
	}
}

type fakeRuntime struct {
	defaultFS sdksandbox.FileSystem
	hostFS    sdksandbox.FileSystem
}

func (f fakeRuntime) Describe() sdksandbox.Descriptor   { return sdksandbox.Descriptor{} }
func (f fakeRuntime) FileSystem() sdksandbox.FileSystem { return f.defaultFS }
func (f fakeRuntime) FileSystemFor(constraints sdksandbox.Constraints) sdksandbox.FileSystem {
	if constraints.Route == sdksandbox.RouteHost || constraints.Permission == sdksandbox.PermissionFullAccess {
		return f.hostFS
	}
	return f.defaultFS
}
func (f fakeRuntime) Run(context.Context, sdksandbox.CommandRequest) (sdksandbox.CommandResult, error) {
	return sdksandbox.CommandResult{}, nil
}
func (f fakeRuntime) Start(context.Context, sdksandbox.CommandRequest) (sdksandbox.Session, error) {
	return nil, nil
}
func (f fakeRuntime) OpenSession(string) (sdksandbox.Session, error) { return nil, nil }
func (f fakeRuntime) OpenSessionRef(sdksandbox.SessionRef) (sdksandbox.Session, error) {
	return nil, nil
}
func (f fakeRuntime) SupportedBackends() []sdksandbox.Backend {
	return []sdksandbox.Backend{sdksandbox.BackendHost}
}
func (f fakeRuntime) Status() sdksandbox.Status {
	return sdksandbox.Status{
		RequestedBackend: sdksandbox.BackendHost,
		ResolvedBackend:  sdksandbox.BackendHost,
	}
}
func (f fakeRuntime) Close() error { return nil }

type fakeFileSystem struct {
	cwd string
}

func (f fakeFileSystem) Getwd() (string, error)                      { return f.cwd, nil }
func (f fakeFileSystem) UserHomeDir() (string, error)                { return "/home/test", nil }
func (f fakeFileSystem) Open(string) (*os.File, error)               { return nil, fs.ErrNotExist }
func (f fakeFileSystem) ReadDir(string) ([]os.DirEntry, error)       { return nil, fs.ErrNotExist }
func (f fakeFileSystem) Stat(string) (os.FileInfo, error)            { return nil, fs.ErrNotExist }
func (f fakeFileSystem) ReadFile(string) ([]byte, error)             { return nil, fs.ErrNotExist }
func (f fakeFileSystem) WriteFile(string, []byte, os.FileMode) error { return nil }
func (f fakeFileSystem) Glob(string) ([]string, error)               { return nil, nil }
func (f fakeFileSystem) WalkDir(string, fs.WalkDirFunc) error        { return nil }

type hostFileSystem struct {
	cwd string
}

func (f hostFileSystem) Getwd() (string, error)                     { return f.cwd, nil }
func (f hostFileSystem) UserHomeDir() (string, error)               { return os.UserHomeDir() }
func (f hostFileSystem) Open(name string) (*os.File, error)         { return os.Open(name) }
func (f hostFileSystem) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }
func (f hostFileSystem) Stat(name string) (os.FileInfo, error)      { return os.Stat(name) }
func (f hostFileSystem) ReadFile(name string) ([]byte, error)       { return os.ReadFile(name) }
func (f hostFileSystem) WriteFile(name string, data []byte, mode os.FileMode) error {
	return os.WriteFile(name, data, mode)
}
func (f hostFileSystem) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }
func (f hostFileSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}
