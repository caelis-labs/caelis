package gatewayapp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

type applicationRunnerProbe struct {
	sandbox.Runtime
	launches int
	writes   int
	files    *applicationFileSystemProbe
}

func (r *applicationRunnerProbe) FileSystem() sandbox.FileSystem { return r.files }

type applicationFileSystemProbe struct {
	sandbox.FileSystem
	writes int
}

func (f *applicationFileSystemProbe) WriteFile(string, []byte, os.FileMode) error {
	f.writes++
	return nil
}

func (r *applicationRunnerProbe) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	r.launches++
	return sandbox.CommandResult{}, nil
}

func applicationDirectoryLink(t *testing.T, path, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", path, target).CombinedOutput(); err != nil {
			t.Fatalf("create fixture junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if linked, err := os.Readlink(path); err != nil || linked == "" {
		t.Fatalf("fixture is not a directory link: %q, %v", linked, err)
	}
}

func TestApplicationDirectoryBindingsRejectRedirection(t *testing.T) {
	for _, target := range []string{"cwd", "read-write", "read-only"} {
		t.Run(target, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cwd, extra, outside := filepath.Join(root, "工作 目录"), filepath.Join(root, "额外 目录"), filepath.Join(root, "outside")
			for _, path := range []string{cwd, extra, outside} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			profile := sessionprofile.Profile{Execution: "workspace-write", Workspace: sessionprofile.Workspace{CWD: cwd}}
			probe := &applicationRunnerProbe{files: &applicationFileSystemProbe{}}
			rt := &applicationExecutionRuntime{Runtime: probe, cwd: cwd}
			bound := cwd
			if target != "cwd" {
				bound = extra
				profile.Workspace.Access = []sessionprofile.WorkspaceAccess{{Path: extra, Mode: target}}
				rt.access = []string{extra}
			}
			opened, err := rt.Start(t.Context(), sandbox.CommandRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(bound, bound+"-original"); err != nil {
				t.Fatal(err)
			}
			applicationDirectoryLink(t, bound, outside)
			if _, err := newApplicationExecutionRuntime(cwd, root, filepath.Join(root, "authority"), profile, nil); err == nil || !strings.Contains(err.Error(), "binding changed") {
				t.Fatalf("activation accepted redirected %s: %v", target, err)
			}
			if _, err := rt.Run(t.Context(), sandbox.CommandRequest{Constraints: sandbox.Constraints{Route: sandbox.RouteHost}}); err == nil {
				t.Fatal("approved Host command accepted redirected binding")
			}
			if _, err := rt.Start(t.Context(), sandbox.CommandRequest{}); err == nil {
				t.Fatal("Start accepted redirected binding")
			}
			if err := opened.WriteInput(t.Context(), []byte("input")); err == nil {
				t.Fatal("interactive input accepted redirected binding")
			}
			if err := rt.FileSystem().WriteFile("effect", []byte("effect"), 0600); err == nil {
				t.Fatal("filesystem effect accepted redirected binding")
			}
			resource := &applicationToolProbe{}
			if _, err := (applicationBoundResourceTool{Tool: resource, runtime: rt}).Call(t.Context(), tool.Call{}); err == nil {
				t.Fatal("resource effect accepted redirected binding")
			}
			if probe.launches != 1 || probe.writes != 0 || probe.files.writes != 0 || resource.calls != 0 {
				t.Fatalf("redirected effects: %+v, resource calls=%d", probe, resource.calls)
			}
		})
	}
}
func (r *applicationRunnerProbe) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	r.launches++
	return applicationSessionProbe{runtime: r}, nil
}
func (r *applicationRunnerProbe) OpenSession(string) (sandbox.Session, error) {
	return applicationSessionProbe{runtime: r}, nil
}
func (r *applicationRunnerProbe) OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error) {
	return applicationSessionProbe{runtime: r}, nil
}

type applicationSessionProbe struct {
	sandbox.Session
	runtime *applicationRunnerProbe
}

func (s applicationSessionProbe) WriteInput(context.Context, []byte) error {
	s.runtime.writes++
	return nil
}

type applicationToolProbe struct{ calls int }

func (t *applicationToolProbe) Definition() tool.Definition {
	return tool.Definition{Name: "ReadResource"}
}
func (t *applicationToolProbe) Call(context.Context, tool.Call) (tool.Result, error) {
	t.calls++
	return tool.Result{Content: []model.Part{model.NewTextPart("effect")}}, nil
}

func TestSessionRuntimeClosurePreventsDelayedNativeEffects(t *testing.T) {
	ctx := t.Context()
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	probe := &applicationRunnerProbe{}
	runtime := &applicationExecutionRuntime{Runtime: probe, cwd: cwd}
	request := sandbox.CommandRequest{Dir: cwd}
	started, err := runtime.Start(ctx, request)
	if err != nil || probe.launches != 1 {
		t.Fatalf("first launch = %d, %v", probe.launches, err)
	}
	opened, err := runtime.OpenSession("already-running")
	if err != nil {
		t.Fatal(err)
	}
	openedByRef, err := runtime.OpenSessionRef(sandbox.SessionRef{})
	if err != nil {
		t.Fatal(err)
	}
	resource := &applicationToolProbe{}
	leased := applicationBoundResourceTool{Tool: resource, runtime: runtime}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	// These calls model a provider delayed beyond Runtime closure and an approval
	// resolved only after closure. No new native launch or resource effect.
	if _, err := runtime.Run(ctx, request); err == nil {
		t.Fatal("closed runtime admitted delayed Run")
	}
	if _, err := runtime.Start(ctx, request); err == nil {
		t.Fatal("closed runtime admitted pending approval Start")
	}
	for _, session := range []sandbox.Session{started, opened, openedByRef} {
		if err := session.WriteInput(ctx, []byte("new command")); err == nil {
			t.Fatal("closed runtime admitted interactive input")
		}
	}
	if _, err := leased.Call(ctx, tool.Call{Name: "ReadResource"}); err == nil {
		t.Fatal("closed runtime admitted resource materialization")
	}
	if probe.launches != 1 || probe.writes != 0 || resource.calls != 0 {
		t.Fatalf("new effects after close: launches=%d writes=%d resource=%d", probe.launches, probe.writes, resource.calls)
	}
}

func (r *applicationRunnerProbe) Close() error { return nil }
