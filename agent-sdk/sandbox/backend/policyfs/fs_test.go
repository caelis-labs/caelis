package policyfs

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
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

func TestWriteProtectsGitMetadataAtDepthOneOnly(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	directGit := filepath.Join(root, "project", ".git")
	deepGit := filepath.Join(root, "container", "project", ".git")
	for _, path := range []string{directGit, deepGit} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}
	fsys := New(testFileSystem{wd: root, home: t.TempDir(), allowWrite: true}, func() policy.Policy {
		return policy.Policy{
			Type:               policy.TypeWorkspaceWrite,
			WritableRoots:      []string{root},
			GitProtectionRoots: []string{root},
			ReadOnlySubpaths:   []string{".git"},
		}
	})
	if err := fsys.WriteFile(filepath.Join(directGit, "index"), nil, 0o600); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("direct child Git write error = %v, want os.ErrPermission", err)
	}
	if err := fsys.WriteFile(filepath.Join(deepGit, "index"), nil, 0o600); err != nil {
		t.Fatalf("depth-two Git write error = %v, want allowed by bounded protection", err)
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

func TestHostReadsRemainBroad(t *testing.T) {
	t.Parallel()

	workspace := deniedAbsoluteTestPath("sandbox-policy-workspace")
	home := deniedAbsoluteTestPath("sandbox-home")
	fsys := New(testFileSystem{
		wd:   workspace,
		home: home,
	}, func() policy.Policy {
		return policy.Default(sandbox.Config{
			CWD: workspace,
		}, sandbox.Constraints{
			Permission: sandbox.PermissionWorkspaceWrite,
			PathRules:  []sandbox.PathRule{{Path: workspace, Access: sandbox.PathAccessReadWrite}},
		})
	})

	for _, target := range []string{
		filepath.Join(home, ".gitconfig"),
		filepath.Join(home, ".config", "git", "ignore"),
		filepath.Join(home, ".g", "go", "bin", "go"),
	} {
		if _, err := fsys.ReadFile(target); err == nil || errors.Is(err, os.ErrPermission) {
			t.Fatalf("ReadFile(%q) error = %v, want underlying filesystem result rather than sandbox denial", target, err)
		}
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

func TestFileAPIsApplyReadCeiling(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "allowed-sibling")
	for _, dir := range []string{allowed, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base, err := host.New(host.Config{CWD: allowed})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	for _, test := range []struct {
		name  string
		roots []string
		allow bool
	}{
		{name: "nil", allow: true},
		{name: "empty", roots: []string{}},
		{name: "explicit", roots: []string{allowed}, allow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fsys := New(base.FileSystem(), func() policy.Policy {
				return policy.Default(sandbox.Config{ResourceLimits: &sandbox.ResourceLimits{
					ReadPaths: test.roots, WritePaths: []string{outside},
				}}, sandbox.Constraints{Route: sandbox.RouteHost, Permission: sandbox.PermissionFullAccess})
			})
			check := func(op, dir string, err error, allowed bool) {
				t.Helper()
				if allowed && err != nil || !allowed && !errors.Is(err, os.ErrPermission) {
					t.Fatalf("%s(%q) error = %v, allowed = %t", op, dir, err, allowed)
				}
			}
			for _, dir := range []string{allowed, outside} {
				allowed := test.allow && (test.roots == nil || dir != outside)
				path := filepath.Join(dir, "input.txt")
				file, err := fsys.Open(path)
				check("Open", dir, err, allowed)
				if file != nil {
					_ = file.Close()
				}
				_, err = fsys.Stat(path)
				check("Stat", dir, err, allowed)
				_, err = fsys.ReadFile(path)
				check("ReadFile", dir, err, allowed)
				_, err = fsys.ReadDir(dir)
				check("ReadDir", dir, err, allowed)
				_, err = fsys.Glob(filepath.Join(dir, "*.txt"))
				check("Glob", dir, err, allowed)
				var seen []string
				err = fsys.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
					seen = append(seen, path)
					return err
				})
				check("WalkDir", dir, err, allowed)
				if allowed && !slices.Contains(seen, path) || !allowed && len(seen) != 0 {
					t.Fatalf("WalkDir(%q) reported %v, allowed = %t", dir, seen, allowed)
				}
			}
			if test.roots != nil && test.allow {
				got, err := fsys.ReadFile("input.txt")
				if err != nil || string(got) != "fixture" {
					t.Fatalf("relative ReadFile = %q, %v", got, err)
				}
			}
		})
	}
}

func TestReadCeilingSupportsIndividualFileGrant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	fsys := New(testFileSystem{wd: dir}, func() policy.Policy {
		return policy.Policy{ResourceLimits: &sandbox.ResourceLimits{ReadPaths: []string{path}}}
	})
	if got, err := fsys.ReadFile("input.txt"); err != nil || string(got) != "fixture" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if _, err := fsys.ReadDir(dir); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("parent directory received read authority: %v", err)
	}
}

type rawOpenFileSystem struct {
	sandbox.FileSystem
	opened *os.File
}

func (f *rawOpenFileSystem) Open(path string) (*os.File, error) {
	file, err := os.Open(path)
	f.opened = file
	return file, err
}

func TestRestrictedOpenClosesRawDirectoryHandle(t *testing.T) {
	root := t.TempDir()
	base, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	raw := &rawOpenFileSystem{FileSystem: base.FileSystem()}
	fsys := New(raw, func() policy.Policy {
		return policy.Policy{HiddenRoots: []string{filepath.Join(root, "hidden")}}
	})
	file, err := fsys.Open(root)
	if file != nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("Open directory = %v, %v", file, err)
	}
	if raw.opened == nil {
		t.Fatal("underlying Open did not return a directory handle")
	}
	if err := raw.opened.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("underlying directory handle is still open: %v", err)
	}
}

func TestReadCeilingFiltersDirectoryEnumeration(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	ordinary := filepath.Join(allowed, "ordinary")
	hidden := filepath.Join(allowed, "hidden")
	for _, dir := range []string{ordinary, hidden, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(allowed, "alias")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	base, err := host.New(host.Config{CWD: allowed})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	fsys := New(base.FileSystem(), func() policy.Policy {
		return policy.Policy{ResourceLimits: &sandbox.ResourceLimits{ReadPaths: []string{allowed}}, HiddenRoots: []string{hidden}}
	})
	if _, err := fsys.ReadFile(filepath.Join(link, "input.txt")); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("ReadFile through an out-of-ceiling directory link = %v", err)
	}
	entries, err := fsys.ReadDir(allowed)
	if err != nil || len(entries) != 1 || entries[0].Name() != "ordinary" {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	want := filepath.Join(ordinary, "input.txt")
	matches, err := fsys.Glob(filepath.Join(allowed, "*", "*.txt"))
	if err != nil || !slices.Equal(matches, []string{want}) {
		t.Fatalf("Glob = %v, %v", matches, err)
	}
	var seen []string
	err = fsys.WalkDir(allowed, func(path string, _ fs.DirEntry, err error) error {
		seen = append(seen, path)
		return err
	})
	if err != nil || !slices.Equal(seen, []string{allowed, ordinary, want}) {
		t.Fatalf("WalkDir = %v, %v", seen, err)
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
