package sandboxrouter

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

type Route struct {
	BackendCandidates   []sandbox.Backend
	FallbackInstallHint string
}

func Current(requested sandbox.Backend) (Route, error) {
	return ForGOOS(runtime.GOOS, requested)
}

func ForGOOS(goos string, requested sandbox.Backend) (Route, error) {
	requested = sandbox.CanonicalBackend(requested)
	switch strings.TrimSpace(goos) {
	case "darwin":
		return routeForPlatform(goos, requested, []sandbox.Backend{sandbox.BackendSeatbelt}, darwinInstallHint())
	case "linux":
		return routeForPlatform(goos, requested, []sandbox.Backend{sandbox.BackendBwrap, sandbox.BackendLandlock}, linuxInstallHint())
	case "windows":
		return routeForPlatform(goos, requested, []sandbox.Backend{sandbox.BackendWindows}, windowsInstallHint())
	default:
		if requested == "" || requested == sandbox.BackendHost {
			return Route{FallbackInstallHint: genericInstallHint(goos)}, nil
		}
		return Route{}, fmt.Errorf("sandbox router: backend %q is unsupported on %s", requested, goos)
	}
}

func routeForPlatform(goos string, requested sandbox.Backend, candidates []sandbox.Backend, hint string) (Route, error) {
	if requested == "" {
		return Route{BackendCandidates: append([]sandbox.Backend(nil), candidates...), FallbackInstallHint: hint}, nil
	}
	if requested == sandbox.BackendHost {
		return Route{FallbackInstallHint: hint}, nil
	}
	for _, candidate := range candidates {
		if requested == candidate {
			return Route{BackendCandidates: []sandbox.Backend{requested}, FallbackInstallHint: hint}, nil
		}
	}
	return Route{}, fmt.Errorf("sandbox router: backend %q is unsupported on %s", requested, goos)
}

func linuxInstallHint() string {
	ids := linuxDistroIDs()
	for _, id := range ids {
		switch id {
		case "debian", "ubuntu", "linuxmint", "pop":
			return "Install bubblewrap with: sudo apt install bubblewrap. If bubblewrap is blocked, Caelis can fall back to Landlock on supported kernels."
		case "fedora", "rhel", "centos", "rocky", "almalinux":
			return "Install bubblewrap with: sudo dnf install bubblewrap. If user namespaces are blocked, enable them or rely on Landlock when supported."
		case "arch", "manjaro":
			return "Install bubblewrap with: sudo pacman -S bubblewrap. If user namespaces are blocked, enable them or rely on Landlock when supported."
		case "opensuse", "suse", "sles":
			return "Install bubblewrap with: sudo zypper install bubblewrap. If user namespaces are blocked, enable them or rely on Landlock when supported."
		}
	}
	return "Install bubblewrap for this distribution or use a Landlock-capable Linux kernel; until then commands may run on the host."
}

func linuxDistroIDs() []string {
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var ids []string
	appendID := func(value string) {
		value = strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ID":
			appendID(value)
		case "ID_LIKE":
			for _, item := range strings.Fields(strings.Trim(strings.TrimSpace(value), `"`)) {
				appendID(item)
			}
		}
	}
	return ids
}

func darwinInstallHint() string {
	return "macOS sandboxing uses sandbox-exec/seatbelt and should be available by default; update macOS if the backend is unavailable."
}

func windowsInstallHint() string {
	return "Windows sandboxing uses a current-user restricted token and workspace ACLs; ACL state is repaired lazily before sandboxed commands run, and `caelis sandbox reset`/`caelis sandbox clean` can remove local sandbox state."
}

func genericInstallHint(goos string) string {
	goos = strings.TrimSpace(goos)
	if goos == "" {
		goos = "this OS"
	}
	return "Install a supported sandbox backend for " + goos + "; until then commands may run on the host."
}
