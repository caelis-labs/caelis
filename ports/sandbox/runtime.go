package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func RegisterBackendFactory(factory BackendFactory) error {
	if factory == nil {
		return fmt.Errorf("ports/sandbox: backend factory is required")
	}
	backend := Backend(strings.TrimSpace(string(factory.Backend())))
	if backend == "" {
		return fmt.Errorf("ports/sandbox: backend factory must declare a backend")
	}
	backendFactoriesMu.Lock()
	defer backendFactoriesMu.Unlock()
	if _, exists := backendFactories[backend]; exists {
		return fmt.Errorf("ports/sandbox: duplicated backend factory %q", backend)
	}
	backendFactories[backend] = factory
	return nil
}

func RegisterBuiltInBackendFactory(factory BackendFactory) {
	if err := RegisterBackendFactory(factory); err != nil {
		backendFactoriesMu.Lock()
		backendRegistrationErrs = append(backendRegistrationErrs, err)
		backendFactoriesMu.Unlock()
	}
}

func backendRegistrationError() error {
	backendFactoriesMu.RLock()
	errs := append([]error(nil), backendRegistrationErrs...)
	backendFactoriesMu.RUnlock()
	return errors.Join(errs...)
}

func New(cfg Config) (Runtime, error) {
	rawRequested := strings.ToLower(strings.TrimSpace(string(cfg.RequestedBackend)))
	autoRequested := rawRequested == "" || rawRequested == "auto" || rawRequested == "default"
	cfg = NormalizeConfig(cfg)

	hostRuntime, err := buildRegisteredRuntime(BackendHost, cfg)
	if err != nil {
		return nil, err
	}
	rt := &compositeRuntime{
		host:     hostRuntime,
		backends: map[Backend]Runtime{BackendHost: hostRuntime},
		status: Status{
			RequestedBackend: cfg.RequestedBackend,
			ResolvedBackend:  BackendHost,
		},
	}

	requested := cfg.RequestedBackend
	if requested == BackendHost {
		rt.status.RequestedBackend = BackendHost
		rt.status.ResolvedBackend = BackendHost
		return rt, nil
	}

	candidates, err := candidateBackends(requested)
	if err != nil {
		_ = hostRuntime.Close()
		return nil, err
	}
	if len(candidates) == 0 {
		_ = hostRuntime.Close()
		return nil, fmt.Errorf("ports/sandbox: no sandbox backend candidates")
	}
	rt.status.RequestedBackend = requested
	rt.status.ResolvedBackend = candidates[0]

	var failures []string
	for _, candidate := range candidates {
		backendRuntime, buildErr := buildRegisteredRuntime(candidate, cfg)
		if buildErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", candidate, buildErr))
			continue
		}
		rt.sandbox = backendRuntime
		rt.backends[candidate] = backendRuntime
		rt.status.ResolvedBackend = candidate
		rt.status.FallbackToHost = false
		rt.status.FallbackReason = strings.Join(failures, "; ")
		if len(failures) > 0 {
			rt.status.FallbackInstallHint = sandboxInstallHint()
		}
		return rt, nil
	}

	if !autoRequested {
		_ = hostRuntime.Close()
		return nil, fmt.Errorf("ports/sandbox: requested backend %q unavailable: %s", requested, strings.Join(failures, "; "))
	}
	rt.status.FallbackToHost = true
	rt.status.ResolvedBackend = BackendHost
	rt.status.FallbackReason = strings.Join(failures, "; ")
	rt.status.FallbackInstallHint = sandboxInstallHint()
	return rt, nil
}

func NormalizeConfig(cfg Config) Config {
	cfg.CWD = strings.TrimSpace(cfg.CWD)
	if cfg.CWD == "" {
		if cwd, err := os.Getwd(); err == nil {
			cfg.CWD = cwd
		}
	}
	if cfg.CWD != "" {
		if abs, err := filepath.Abs(cfg.CWD); err == nil {
			cfg.CWD = abs
		}
	}
	switch strings.ToLower(strings.TrimSpace(string(cfg.RequestedBackend))) {
	case "", "auto", "default":
		cfg.RequestedBackend = ""
	default:
		cfg.RequestedBackend = Backend(strings.TrimSpace(string(cfg.RequestedBackend)))
	}
	cfg.HelperPath = strings.TrimSpace(cfg.HelperPath)
	cfg.StateDir = strings.TrimSpace(cfg.StateDir)
	if cfg.StateDir != "" {
		if abs, err := filepath.Abs(cfg.StateDir); err == nil {
			cfg.StateDir = abs
		}
	}
	cfg.ReadableRoots = normalizeStringSlice(cfg.ReadableRoots)
	cfg.WritableRoots = normalizeStringSlice(cfg.WritableRoots)
	cfg.ReadOnlySubpaths = normalizeStringSlice(cfg.ReadOnlySubpaths)
	return cfg
}

func buildRegisteredRuntime(backend Backend, cfg Config) (Runtime, error) {
	backendFactoriesMu.RLock()
	factory, ok := backendFactories[backend]
	backendFactoriesMu.RUnlock()
	if !ok {
		if err := backendRegistrationError(); err != nil {
			return nil, fmt.Errorf("ports/sandbox: backend %q is not registered: %w", backend, err)
		}
		return nil, fmt.Errorf("ports/sandbox: backend %q is not registered", backend)
	}
	runtime, err := factory.Build(cfg)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, fmt.Errorf("ports/sandbox: backend %q returned a nil runtime", backend)
	}
	return runtime, nil
}

func candidateBackends(requested Backend) ([]Backend, error) {
	return candidateBackendsForGOOS(runtime.GOOS, requested)
}

func candidateBackendsForGOOS(goos string, requested Backend) ([]Backend, error) {
	requested = Backend(strings.TrimSpace(string(requested)))
	switch goos {
	case "darwin":
		if requested == "" {
			return []Backend{BackendSeatbelt}, nil
		}
		if requested != BackendSeatbelt && requested != BackendHost {
			return nil, fmt.Errorf("ports/sandbox: backend %q is unsupported on darwin", requested)
		}
		return []Backend{requested}, nil
	case "linux":
		if requested == "" {
			return []Backend{BackendBwrap, BackendLandlock}, nil
		}
		if requested != BackendBwrap && requested != BackendLandlock && requested != BackendHost {
			return nil, fmt.Errorf("ports/sandbox: backend %q is unsupported on linux", requested)
		}
		return []Backend{requested}, nil
	case "windows":
		if requested == "" {
			return []Backend{BackendWindowsElevated}, nil
		}
		if requested != BackendWindowsElevated && requested != BackendHost {
			return nil, fmt.Errorf("ports/sandbox: backend %q is unsupported on windows", requested)
		}
		return []Backend{requested}, nil
	default:
		if requested == "" {
			return nil, nil
		}
		if requested != BackendHost {
			return nil, fmt.Errorf("ports/sandbox: backend %q is unsupported on %s", requested, goos)
		}
		return []Backend{requested}, nil
	}
}

func sandboxInstallHint() string {
	switch runtime.GOOS {
	case "linux":
		return linuxSandboxInstallHint()
	case "darwin":
		return "macOS sandboxing uses sandbox-exec/seatbelt and should be available by default; update macOS if the backend is unavailable."
	case "windows":
		return "Run `caelis sandbox setup` or TUI `/sandbox setup` once to initialize Windows Elevated sandbox."
	default:
		return "Install a supported sandbox backend for this OS; until then commands may run on the host."
	}
}

func linuxSandboxInstallHint() string {
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

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
