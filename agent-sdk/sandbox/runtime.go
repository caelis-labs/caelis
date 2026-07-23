package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	backendFactoryOrder = append(backendFactoryOrder, backend)
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
	autoRequested := CanonicalBackend(cfg.RequestedBackend) == ""
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

	candidates, err := candidateBackends(cfg)
	if err != nil {
		_ = hostRuntime.Close()
		return nil, err
	}
	if len(candidates) == 0 {
		rt.status.FallbackToHost = true
		rt.status.ResolvedBackend = BackendHost
		rt.status.FallbackReason = "no sandbox backend candidates"
		rt.status.FallbackInstallHint = fallbackInstallHint(cfg)
		return rt, nil
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
			rt.status.FallbackInstallHint = fallbackInstallHint(cfg)
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
	rt.status.FallbackInstallHint = fallbackInstallHint(cfg)
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
	cfg.RequestedBackend = CanonicalBackend(cfg.RequestedBackend)
	cfg.HelperPath = strings.TrimSpace(cfg.HelperPath)
	cfg.StateDir = strings.TrimSpace(cfg.StateDir)
	if cfg.StateDir != "" {
		if abs, err := filepath.Abs(cfg.StateDir); err == nil {
			cfg.StateDir = abs
		}
	}
	cfg.WritableRoots = normalizeStringSlice(cfg.WritableRoots)
	cfg.ReadOnlySubpaths = normalizeStringSlice(cfg.ReadOnlySubpaths)
	cfg.BackendCandidates = normalizeBackendCandidates(cfg.BackendCandidates)
	cfg.FallbackInstallHint = strings.TrimSpace(cfg.FallbackInstallHint)
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

func candidateBackends(cfg Config) ([]Backend, error) {
	requested := CanonicalBackend(cfg.RequestedBackend)
	if requested != "" {
		return []Backend{requested}, nil
	}
	if len(cfg.BackendCandidates) > 0 {
		return normalizeBackendCandidates(cfg.BackendCandidates), nil
	}
	backendFactoriesMu.RLock()
	order := append([]Backend(nil), backendFactoryOrder...)
	factories := make(map[Backend]BackendFactory, len(backendFactories))
	for backend, factory := range backendFactories {
		factories[backend] = factory
	}
	backendFactoriesMu.RUnlock()
	var out []Backend
	seen := map[Backend]struct{}{}
	for _, backend := range order {
		if backend == "" || backend == BackendHost {
			continue
		}
		if _, ok := factories[backend]; !ok {
			continue
		}
		if _, ok := seen[backend]; ok {
			continue
		}
		seen[backend] = struct{}{}
		out = append(out, backend)
	}
	return out, nil
}

func fallbackInstallHint(cfg Config) string {
	if hint := strings.TrimSpace(cfg.FallbackInstallHint); hint != "" {
		return hint
	}
	return "Install or enable a supported sandbox backend for this environment; until then commands may run on the host."
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
