package sandboxrouter

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestForGOOSWindowsDefaultsToSandbox(t *testing.T) {
	route, err := ForGOOS("windows", "")
	if err != nil {
		t.Fatalf("ForGOOS(windows, auto) error = %v", err)
	}
	if len(route.BackendCandidates) != 1 || route.BackendCandidates[0] != sandbox.BackendWindows {
		t.Fatalf("BackendCandidates = %v, want [%s]", route.BackendCandidates, sandbox.BackendWindows)
	}
	if strings.TrimSpace(route.FallbackInstallHint) == "" {
		t.Fatal("FallbackInstallHint is empty")
	}
}

func TestForGOOSWindowsElevatedAliasResolvesToWindows(t *testing.T) {
	route, err := ForGOOS("windows", sandbox.BackendWindowsElevated)
	if err != nil {
		t.Fatalf("ForGOOS(windows, legacy alias) error = %v", err)
	}
	if len(route.BackendCandidates) != 1 || route.BackendCandidates[0] != sandbox.BackendWindows {
		t.Fatalf("BackendCandidates = %v, want [%s]", route.BackendCandidates, sandbox.BackendWindows)
	}
	if strings.TrimSpace(route.FallbackInstallHint) == "" {
		t.Fatal("FallbackInstallHint is empty")
	}
}

func TestForGOOSWindowsHostIsExplicit(t *testing.T) {
	route, err := ForGOOS("windows", sandbox.BackendHost)
	if err != nil {
		t.Fatalf("ForGOOS(windows, host) error = %v", err)
	}
	if len(route.BackendCandidates) != 0 {
		t.Fatalf("BackendCandidates = %v, want none for explicit host execution", route.BackendCandidates)
	}
}

func TestForGOOSRejectsUnsupportedBackend(t *testing.T) {
	_, err := ForGOOS("windows", sandbox.BackendBwrap)
	if err == nil {
		t.Fatal("ForGOOS(windows, bwrap) error = nil, want unsupported backend error")
	}
	if !strings.Contains(err.Error(), "unsupported on windows") {
		t.Fatalf("ForGOOS(windows, bwrap) error = %v, want unsupported on windows", err)
	}
}

func TestForGOOSLinuxOrdersBwrapBeforeLandlock(t *testing.T) {
	route, err := ForGOOS("linux", "")
	if err != nil {
		t.Fatalf("ForGOOS(linux, auto) error = %v", err)
	}
	want := []sandbox.Backend{sandbox.BackendBwrap, sandbox.BackendLandlock}
	if len(route.BackendCandidates) != len(want) {
		t.Fatalf("BackendCandidates = %v, want %v", route.BackendCandidates, want)
	}
	for i := range want {
		if route.BackendCandidates[i] != want[i] {
			t.Fatalf("BackendCandidates = %v, want %v", route.BackendCandidates, want)
		}
	}
}
