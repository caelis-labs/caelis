package sandboxrouter

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

type Route struct {
	Backend           sandbox.Backend
	BackendCandidates []sandbox.Backend
	InstallHint       string
}

func Current(requested sandbox.Backend) (Route, error) {
	return ForGOOS(runtime.GOOS, requested)
}

func ForGOOS(goos string, requested sandbox.Backend) (Route, error) {
	requested = sandbox.CanonicalBackend(requested)
	switch strings.TrimSpace(goos) {
	case "darwin":
		return routeForPlatform(goos, requested, sandbox.BackendSeatbelt, darwinInstallHint())
	case "linux":
		return routeForPlatform(goos, requested, sandbox.BackendBwrap, linuxInstallHint())
	case "windows":
		return routeForPlatform(goos, requested, sandbox.BackendWindows, windowsInstallHint())
	default:
		if requested == sandbox.BackendHost {
			return Route{Backend: sandbox.BackendHost, InstallHint: genericInstallHint(goos)}, nil
		}
		return Route{}, fmt.Errorf("sandbox router: no supported platform sandbox is available on %s; %s", goos, genericInstallHint(goos))
	}
}

func routeForPlatform(goos string, requested sandbox.Backend, platformBackend sandbox.Backend, hint string) (Route, error) {
	if requested == "" {
		return Route{Backend: platformBackend, BackendCandidates: []sandbox.Backend{platformBackend}, InstallHint: hint}, nil
	}
	if requested == sandbox.BackendHost {
		return Route{Backend: sandbox.BackendHost, InstallHint: hint}, nil
	}
	if requested == platformBackend {
		return Route{Backend: requested, BackendCandidates: []sandbox.Backend{requested}, InstallHint: hint}, nil
	}
	return Route{}, fmt.Errorf(
		"sandbox router: backend %q is unsupported on %s; use %q or choose explicit Host execution",
		requested,
		goos,
		platformBackend,
	)
}

func linuxInstallHint() string {
	ids := linuxDistroIDs()
	for _, id := range ids {
		switch id {
		case "debian", "ubuntu", "linuxmint", "pop":
			return "Install bubblewrap with: sudo apt install bubblewrap, ensure unprivileged user namespaces are enabled, then retry."
		case "fedora", "rhel", "centos", "rocky", "almalinux":
			return "Install bubblewrap with: sudo dnf install bubblewrap, ensure unprivileged user namespaces are enabled, then retry."
		case "arch", "manjaro":
			return "Install bubblewrap with: sudo pacman -S bubblewrap, ensure unprivileged user namespaces are enabled, then retry."
		case "opensuse", "suse", "sles":
			return "Install bubblewrap with: sudo zypper install bubblewrap, ensure unprivileged user namespaces are enabled, then retry."
		}
	}
	return "Install bubblewrap for this distribution, ensure unprivileged user namespaces are enabled, then retry."
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
	return "Ensure this process and any outer container or security profile permit /usr/bin/sandbox-exec, then retry; update macOS if Seatbelt is unavailable."
}

func windowsInstallHint() string {
	return "Windows sandboxing uses a current-user restricted token and workspace ACLs; ACL state is repaired lazily before sandboxed commands run, and `caelis sandbox reset`/`caelis sandbox clean` can remove local sandbox state."
}

func genericInstallHint(goos string) string {
	goos = strings.TrimSpace(goos)
	if goos == "" {
		goos = "this OS"
	}
	return "use macOS, Linux, or Windows with its required sandbox backend; " + goos + " has no supported platform sandbox and Caelis will not fall back to Host execution"
}
