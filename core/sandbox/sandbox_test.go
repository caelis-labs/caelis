package sandbox

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCloneRequestIsolatesMutableFields(t *testing.T) {
	req := CommandRequest{
		Command: " echo ok ",
		Dir:     " . ",
		Env:     map[string]string{"A": "1"},
		Stdin:   []byte("input"),
		Constraints: Constraints{
			Route: RouteSandbox,
			PathRules: []PathRule{{
				Path:   " /tmp/work ",
				Access: PathReadWrite,
			}},
		},
	}

	cloned := CloneRequest(req)
	req.Env["A"] = "2"
	req.Stdin[0] = 'X'
	req.Constraints.PathRules[0].Path = "/changed"

	if cloned.Command != "echo ok" || cloned.Dir != "." {
		t.Fatalf("CloneRequest() command/dir = %q/%q, want trimmed", cloned.Command, cloned.Dir)
	}
	if cloned.Env["A"] != "1" || string(cloned.Stdin) != "input" {
		t.Fatalf("CloneRequest() did not isolate mutable fields: env=%v stdin=%q", cloned.Env, cloned.Stdin)
	}
	if got := cloned.Constraints.PathRules[0].Path; got != "/tmp/work" {
		t.Fatalf("CloneRequest() path rule = %q, want trimmed original", got)
	}
}

func TestNormalizeConfigCanonicalizesBackendAndPaths(t *testing.T) {
	cfg := NormalizeConfig(Config{
		CWD:              ".",
		RequestedBackend: Backend("windows elevated"),
		StateDir:         ".state",
		Network:          Network(" off "),
		ReadableRoots:    []string{" /tmp/read ", "/tmp/read", ""},
		WritableRoots:    []string{" /tmp/write "},
		BackendCandidates: []Backend{
			BackendHost,
			BackendSeatbelt,
			BackendSeatbelt,
			BackendBwrap,
		},
	})

	if cfg.RequestedBackend != BackendWindows {
		t.Fatalf("RequestedBackend = %q, want windows", cfg.RequestedBackend)
	}
	if !filepath.IsAbs(cfg.CWD) || !filepath.IsAbs(cfg.StateDir) {
		t.Fatalf("CWD/StateDir = %q/%q, want absolute paths", cfg.CWD, cfg.StateDir)
	}
	if got := cfg.ReadableRoots; len(got) != 1 || got[0] != "/tmp/read" {
		t.Fatalf("ReadableRoots = %#v, want deduped trimmed root", got)
	}
	if cfg.Network != NetworkDisabled {
		t.Fatalf("Network = %q, want disabled", cfg.Network)
	}
	if got := cfg.BackendCandidates; len(got) != 2 || got[0] != BackendSeatbelt || got[1] != BackendBwrap {
		t.Fatalf("BackendCandidates = %#v, want non-host deduped candidates", got)
	}
}

func TestSandboxPermissionDetailPreservesSpecificDiagnostics(t *testing.T) {
	result := CommandResult{
		Stderr:   "cannot lock config file /work/.git/config: Read-only file system",
		ExitCode: 1,
		Route:    RouteSandbox,
		Backend:  BackendBwrap,
	}

	detail, ok := SandboxPermissionDetail(result, errors.New("exit status 1"))
	if !ok {
		t.Fatal("SandboxPermissionDetail() ok = false, want permission detail")
	}
	if detail != SandboxPermissionDeniedMessage {
		t.Fatalf("SandboxPermissionDetail() = %q, want normalized public message", detail)
	}
}
