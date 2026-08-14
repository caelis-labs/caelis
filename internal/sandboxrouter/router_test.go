package sandboxrouter

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestForGOOSWindowsDefaultsToSandbox(t *testing.T) {
	route, err := ForGOOS("windows", "")
	if err != nil {
		t.Fatalf("ForGOOS(windows, auto) error = %v", err)
	}
	if len(route.BackendCandidates) != 1 || route.BackendCandidates[0] != sandbox.BackendWindows {
		t.Fatalf("BackendCandidates = %v, want [%s]", route.BackendCandidates, sandbox.BackendWindows)
	}
	if route.Backend != sandbox.BackendWindows {
		t.Fatalf("Backend = %q, want %q", route.Backend, sandbox.BackendWindows)
	}
	if strings.TrimSpace(route.InstallHint) == "" {
		t.Fatal("InstallHint is empty")
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
	if route.Backend != sandbox.BackendWindows {
		t.Fatalf("Backend = %q, want %q", route.Backend, sandbox.BackendWindows)
	}
	if strings.TrimSpace(route.InstallHint) == "" {
		t.Fatal("InstallHint is empty")
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
	if route.Backend != sandbox.BackendHost {
		t.Fatalf("Backend = %q, want host", route.Backend)
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

func TestForGOOSUsesOneRequiredBackendPerPlatform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		goos string
		want sandbox.Backend
	}{
		{goos: "darwin", want: sandbox.BackendSeatbelt},
		{goos: "linux", want: sandbox.BackendBwrap},
		{goos: "windows", want: sandbox.BackendWindows},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.goos, func(t *testing.T) {
			t.Parallel()
			for _, legacy := range []sandbox.Backend{"", "auto", "default"} {
				route, err := ForGOOS(tt.goos, legacy)
				if err != nil {
					t.Fatalf("ForGOOS(%s, %q) error = %v", tt.goos, legacy, err)
				}
				if route.Backend != tt.want || len(route.BackendCandidates) != 1 || route.BackendCandidates[0] != tt.want {
					t.Fatalf("ForGOOS(%s, %q) route = %#v, want only %q", tt.goos, legacy, route, tt.want)
				}
				if hint := strings.ToLower(strings.TrimSpace(route.InstallHint)); hint == "" || strings.Contains(hint, "fall back") {
					t.Fatalf("ForGOOS(%s, %q) repair hint = %q, want non-fallback guidance", tt.goos, legacy, route.InstallHint)
				}
			}
		})
	}
}

func TestForGOOSLinuxRejectsLandlockInsteadOfFallingBack(t *testing.T) {
	route, err := ForGOOS("linux", "")
	if err != nil {
		t.Fatalf("ForGOOS(linux, auto) error = %v", err)
	}
	if route.Backend != sandbox.BackendBwrap || len(route.BackendCandidates) != 1 || route.BackendCandidates[0] != sandbox.BackendBwrap {
		t.Fatalf("ForGOOS(linux, auto) = %#v, want only bwrap", route)
	}
	if _, err := ForGOOS("linux", sandbox.BackendLandlock); err == nil || !strings.Contains(err.Error(), "unsupported on linux") {
		t.Fatalf("ForGOOS(linux, landlock) error = %v, want unsupported", err)
	}
}

func TestForGOOSUnsupportedPlatformFailsClosed(t *testing.T) {
	_, err := ForGOOS("plan9", "auto")
	if err == nil || !strings.Contains(err.Error(), "no supported platform sandbox") || !strings.Contains(err.Error(), "will not fall back") {
		t.Fatalf("ForGOOS(plan9, auto) error = %v, want fail-closed repair guidance", err)
	}
}
