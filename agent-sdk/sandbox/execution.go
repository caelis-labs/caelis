package sandbox

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
)

// CloneExecutionConfig returns an independent copy, preserving nil and empty
// collections and the distinction between nil and false Inherit.
func CloneExecutionConfig(in *ExecutionConfig) *ExecutionConfig {
	if in == nil {
		return nil
	}
	out := *in
	if in.Environment.Inherit != nil {
		value := *in.Environment.Inherit
		out.Environment.Inherit = &value
	}
	out.Environment.Set = maps.Clone(in.Environment.Set)
	out.Environment.Unset = slices.Clone(in.Environment.Unset)
	return &out
}

// CloneConfig returns an independent copy of mutable sandbox configuration.
func CloneConfig(in Config) Config {
	out := in
	out.Execution = CloneExecutionConfig(in.Execution)
	out.BaseEnv = slices.Clone(in.BaseEnv)
	if in.ResourceLimits != nil {
		limits := *in.ResourceLimits
		limits.ReadPaths = slices.Clone(in.ResourceLimits.ReadPaths)
		limits.WritePaths = slices.Clone(in.ResourceLimits.WritePaths)
		out.ResourceLimits = &limits
	}
	out.BackendCandidates = slices.Clone(in.BackendCandidates)
	out.WritableRoots = slices.Clone(in.WritableRoots)
	out.ReadOnlySubpaths = slices.Clone(in.ReadOnlySubpaths)
	return out
}

// ValidateExecutionConfig rejects unsupported shell options and invalid
// environment variable names and values before starting a runtime.
func ValidateExecutionConfig(cfg *ExecutionConfig) error {
	if cfg == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		if cfg.Shell.Path != "" || cfg.Shell.Login {
			return fmt.Errorf("sandbox: custom or login shells are unsupported on Windows")
		}
	} else if cfg.Shell.Path != "" && (!filepath.IsAbs(cfg.Shell.Path) || strings.ContainsRune(cfg.Shell.Path, 0)) {
		return fmt.Errorf("sandbox: shell path must be an absolute POSIX path")
	}
	return ValidateCommandEnvironment(CommandRequest{Env: cfg.Environment.Set, UnsetEnv: cfg.Environment.Unset})
}

// ValidateConfig validates execution settings including mandatory resource
// limits, which must not permit login initialization.
func ValidateConfig(cfg Config) error {
	if err := ValidateExecutionConfig(cfg.Execution); err != nil {
		return err
	}
	if cfg.ResourceLimits != nil && cfg.Execution != nil && cfg.Execution.Shell.Login {
		return fmt.Errorf("sandbox: login shell is incompatible with mandatory resource limits")
	}
	return nil
}

// ValidateCommandEnvironment rejects invalid per-command variable names and
// values before any process is started on any route.
func ValidateCommandEnvironment(req CommandRequest) error {
	if err := validateEnvironmentSet(req.Env); err != nil {
		return err
	}
	for name, value := range req.Env {
		if err := validateEnvName(name); err != nil {
			return err
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("sandbox: environment value for %q contains NUL", name)
		}
	}
	for _, name := range req.UnsetEnv {
		if err := validateEnvName(name); err != nil {
			return err
		}
	}
	return nil
}

func validateEnvironmentSet(set map[string]string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	seen := make(map[string]string, len(set))
	for name := range set {
		key := strings.ToUpper(name)
		if previous, ok := seen[key]; ok && previous != name {
			return fmt.Errorf("sandbox: environment variable names %q and %q differ only by case on Windows", previous, name)
		}
		seen[key] = name
	}
	return nil
}

func validateEnvName(name string) error {
	if name == "" || strings.ContainsAny(name, "=\x00") {
		return fmt.Errorf("sandbox: invalid environment variable name %q", name)
	}
	return nil
}

// EnvironmentSnapshot holds the host environment sampled once when a runtime
// is created, along with its immutable execution settings. Never mutate it.
type EnvironmentSnapshot struct {
	base []string
	cfg  *ExecutionConfig
}

// NewEnvironmentSnapshot samples the ambient environment once if baseEnv is nil.
// A non-nil empty baseEnv remains empty. ValidateConfig before calling this
// from a runtime constructor.
func NewEnvironmentSnapshot(cfg *ExecutionConfig, baseEnv []string) EnvironmentSnapshot {
	base := baseEnv
	if base == nil {
		base = os.Environ()
	}
	return EnvironmentSnapshot{base: slices.Clone(base), cfg: CloneExecutionConfig(cfg)}
}

// Build applies config unset/set, then command unset/set in that order.
// Callers must validate config and command values before calling Build.
// Its result is always non-nil, including for an intentionally empty env.
func (s EnvironmentSnapshot) Build(unset []string, set map[string]string) []string {
	values := make(map[string]string)
	keyName := func(key string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(key)
		}
		return key
	}
	put := func(key, value string) { values[keyName(key)] = key + "=" + value }
	remove := func(key string) { delete(values, keyName(key)) }
	if s.cfg == nil || s.cfg.Environment.Inherit == nil || *s.cfg.Environment.Inherit {
		for _, item := range s.base {
			key, value, ok := strings.Cut(item, "=")
			if ok && key != "" {
				put(key, value)
			}
		}
	}
	if s.cfg != nil {
		for _, key := range s.cfg.Environment.Unset {
			remove(key)
		}
		for key, value := range s.cfg.Environment.Set {
			put(key, value)
		}
	}
	for _, key := range unset {
		remove(key)
	}
	for key, value := range set {
		if key != "" {
			put(key, value)
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(values))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

// ForCommand assembles the environment for one command from this snapshot.
func (s EnvironmentSnapshot) ForCommand(req CommandRequest) []string {
	return s.Build(req.UnsetEnv, req.Env)
}

// CommandDirectory resolves an omitted or relative request directory against
// the runtime CWD, without changing the process working directory.
func CommandDirectory(cwd, dir string) string {
	if dir == "" {
		return cwd
	}
	if !filepath.IsAbs(dir) && cwd != "" {
		return filepath.Join(cwd, dir)
	}
	return dir
}

// ShellArgs returns the configured POSIX shell and command arguments. Windows
// callers use their existing PowerShell command adapter instead.
func ShellArgs(cfg *ExecutionConfig, command string) (string, []string) {
	path, flag := "/bin/bash", "-c"
	if cfg != nil {
		if cfg.Shell.Path != "" {
			path = cfg.Shell.Path
		}
		if cfg.Shell.Login {
			flag = "-lc"
		}
	}
	return path, []string{flag, command}
}
