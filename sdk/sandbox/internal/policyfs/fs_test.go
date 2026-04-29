package policyfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/policy"
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
	if !strings.Contains(err.Error(), sdksandbox.SandboxPermissionDeniedMessage) {
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
		return policy.Default(sdksandbox.Config{
			CWD: deniedAbsoluteTestPath("sandbox-cwd"),
		}, sdksandbox.Constraints{
			Permission: sdksandbox.PermissionWorkspaceWrite,
			PathRules: []sdksandbox.PathRule{
				{Path: workspace, Access: sdksandbox.PathAccessReadWrite},
			},
		})
	})

	if err := fsys.WriteFile(target, []byte("package workflow\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v, want allowed by constraint path rule", err)
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
