package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func RegisterBackendFactory(factory BackendFactory) error {
	if factory == nil {
		return fmt.Errorf("sdk/sandbox: backend factory is required")
	}
	backend := Backend(strings.TrimSpace(string(factory.Backend())))
	if backend == "" {
		return fmt.Errorf("sdk/sandbox: backend factory must declare a backend")
	}
	backendFactoriesMu.Lock()
	defer backendFactoriesMu.Unlock()
	if _, exists := backendFactories[backend]; exists {
		return fmt.Errorf("sdk/sandbox: duplicated backend factory %q", backend)
	}
	backendFactories[backend] = factory
	return nil
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
		return nil, fmt.Errorf("sdk/sandbox: no sandbox backend candidates")
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
		rt.status.FallbackReason = ""
		return rt, nil
	}

	if !autoRequested {
		_ = hostRuntime.Close()
		return nil, fmt.Errorf("sdk/sandbox: requested backend %q unavailable: %s", requested, strings.Join(failures, "; "))
	}
	rt.status.FallbackToHost = true
	rt.status.ResolvedBackend = BackendHost
	rt.status.FallbackReason = strings.Join(failures, "; ")
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
		return nil, fmt.Errorf("sdk/sandbox: backend %q is not registered", backend)
	}
	runtime, err := factory.Build(cfg)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, fmt.Errorf("sdk/sandbox: backend %q returned a nil runtime", backend)
	}
	return runtime, nil
}

func candidateBackends(requested Backend) ([]Backend, error) {
	requested = Backend(strings.TrimSpace(string(requested)))
	switch runtime.GOOS {
	case "darwin":
		if requested == "" {
			return []Backend{BackendSeatbelt}, nil
		}
		if requested != BackendSeatbelt && requested != BackendHost {
			return nil, fmt.Errorf("sdk/sandbox: backend %q is unsupported on darwin", requested)
		}
		return []Backend{requested}, nil
	case "linux":
		if requested == "" {
			return []Backend{BackendBwrap, BackendLandlock}, nil
		}
		if requested != BackendBwrap && requested != BackendLandlock && requested != BackendHost {
			return nil, fmt.Errorf("sdk/sandbox: backend %q is unsupported on linux", requested)
		}
		return []Backend{requested}, nil
	default:
		if requested == "" {
			return []Backend{BackendBwrap}, nil
		}
		return []Backend{requested}, nil
	}
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
