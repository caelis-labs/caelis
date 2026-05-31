package policyfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/internal/policy"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

type testFileSystem struct {
	wd         string
	home       string
	allowWrite bool
}

func (f testFileSystem) Getwd() (string, error)       { return f.wd, nil }
func (f testFileSystem) UserHomeDir() (string, error) { return f.home, nil }
func (f testFileSystem) Open(string) (*os.File, error) {
	return nil, errors.New("unexpected Open")
}
func (f testFileSystem) ReadDir(string) ([]os.DirEntry, error) {
	return nil, errors.New("unexpected ReadDir")
}
func (f testFileSystem) Stat(string) (os.FileInfo, error) {
	return nil, errors.New("unexpected Stat")
}
func (f testFileSystem) ReadFile(string) ([]byte, error) {
	return nil, errors.New("unexpected ReadFile")
}
func (f testFileSystem) WriteFile(string, []byte, os.FileMode) error {
	if f.allowWrite {
		return nil
	}
	return errors.New("unexpected WriteFile")
}
func (f testFileSystem) Glob(string) ([]string, error) {
	return nil, errors.New("unexpected Glob")
}
func (f testFileSystem) WalkDir(string, fs.WalkDirFunc) error {
	return errors.New("unexpected WalkDir")
}

func TestWriteDeniedReturnsSandboxPermissionWithoutHostPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	deniedHome := deniedAbsoluteTestPath("sandbox-denied-home")
	target := filepath.Join(deniedHome, ".gitconfig")
	fsys := New(testFileSystem{
		wd:   filepath.Join(root, "workspace"),
		home: deniedHome,
	}, func() policy.Policy {
		return policy.Policy{
			Type:          policy.TypeWorkspaceWrite,
			WritableRoots: []string{filepath.Join(root, "workspace")},
		}
	})

	err := fsys.WriteFile(target, []byte("user.name=test\n"), 0o600)
	if err == nil {
		t.Fatal("WriteFile() error = nil, want sandbox permission error")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("WriteFile() error = %v, want os.ErrPermission", err)
	}
	if strings.Contains(err.Error(), target) || strings.Contains(err.Error(), deniedHome) {
		t.Fatalf("WriteFile() leaked denied host path: %q", err.Error())
	}
	if !strings.Contains(err.Error(), sandbox.SandboxPermissionDeniedMessage) {
		t.Fatalf("WriteFile() error = %q, want sandbox permission message", err.Error())
	}
}

func TestWriteAllowedByConstraintPathRule(t *testing.T) {
	t.Parallel()

	workspace := deniedAbsoluteTestPath("sandbox-policy-workspace")
	target := filepath.Join(workspace, "workflow.go")
	fsys := New(testFileSystem{
		wd:         deniedAbsoluteTestPath("sandbox-cwd"),
		home:       deniedAbsoluteTestPath("sandbox-home"),
		allowWrite: true,
	}, func() policy.Policy {
		return policy.Default(sandbox.Config{
			CWD: deniedAbsoluteTestPath("sandbox-cwd"),
		}, sandbox.Constraints{
			Permission: sandbox.PermissionWorkspaceWrite,
			PathRules: []sandbox.PathRule{
				{Path: workspace, Access: sandbox.PathAccessReadWrite},
			},
		})
	})

	if err := fsys.WriteFile(target, []byte("package workflow\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v, want allowed by constraint path rule", err)
	}
}

func TestWriteAllowedWhenExplicitGrantOverridesDefaultGitReadOnlySubpath(t *testing.T) {
	t.Parallel()

	workspace := deniedAbsoluteTestPath("sandbox-policy-workspace")
	target := filepath.Join(workspace, ".git", "index.lock")
	fsys := New(testFileSystem{
		wd:         workspace,
		home:       deniedAbsoluteTestPath("sandbox-home"),
		allowWrite: true,
	}, func() policy.Policy {
		return policy.Default(sandbox.Config{
			CWD: workspace,
		}, sandbox.Constraints{
			Permission: sandbox.PermissionWorkspaceWrite,
			PathRules: []sandbox.PathRule{
				{Path: workspace, Access: sandbox.PathAccessReadWrite},
				{Path: filepath.Join(workspace, ".git"), Access: sandbox.PathAccessReadWrite},
			},
		})
	})

	if err := fsys.WriteFile(target, []byte("lock"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v, want allowed by explicit .git write grant", err)
	}
}

func TestHiddenPathRuleDeniesReadAndWrite(t *testing.T) {
	t.Parallel()

	hidden := deniedAbsoluteTestPath("sandbox-hidden")
	target := filepath.Join(hidden, "token")
	fsys := New(testFileSystem{
		wd:   deniedAbsoluteTestPath("sandbox-cwd"),
		home: deniedAbsoluteTestPath("sandbox-home"),
	}, func() policy.Policy {
		return policy.Default(sandbox.Config{
			CWD: deniedAbsoluteTestPath("sandbox-cwd"),
		}, sandbox.Constraints{
			Permission: sandbox.PermissionWorkspaceWrite,
			PathRules: []sandbox.PathRule{
				{Path: hidden, Access: sandbox.PathAccessReadOnly},
				{Path: hidden, Access: sandbox.PathAccessReadWrite},
				{Path: hidden, Access: sandbox.PathAccessHidden},
			},
		})
	})

	if _, err := fsys.ReadFile(target); err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("ReadFile() error = %v, want permission", err)
	}
	if err := fsys.WriteFile(target, []byte("x"), 0o600); err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("WriteFile() error = %v, want permission", err)
	}
}

func deniedAbsoluteTestPath(elem ...string) string {
	volume := filepath.VolumeName(os.TempDir())
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	return filepath.Join(append([]string{root}, elem...)...)
}
