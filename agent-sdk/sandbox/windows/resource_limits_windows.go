//go:build windows

package windows

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
)

// normalizeResourceLimits gives command and filesystem policy the same fixed
// write authority. The first root also holds the restricted process environment.
func normalizeResourceLimits(cfg Config) (Config, error) {
	if cfg.ResourceLimits == nil {
		return cfg, nil
	}
	roots := pathutil.Dedupe(cfg.ResourceLimits.WritePaths)
	if len(roots) == 0 {
		return Config{}, fmt.Errorf("sandbox: Windows resource limits require a writable directory")
	}
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return Config{}, fmt.Errorf("sandbox: inspect Windows resource limit directory: %w", err)
		}
		if !info.IsDir() {
			return Config{}, fmt.Errorf("sandbox: Windows resource limit path must be a directory: %s", root)
		}
	}
	cfg.ResourceLimits = &sandbox.ResourceLimits{WritePaths: roots, Network: sandbox.NetworkEnabled}
	cfg.WritableRoots = append([]string(nil), roots...)
	cfg.ReadOnlySubpaths = nil
	return cfg, nil
}

func (r *runtime) validateResourceLimitEnvironment(root string) error {
	if r.cfg.ResourceLimits == nil {
		return nil
	}
	paths := append(sandboxEnvDirs(root), filepath.Join(sandboxPythonSiteDir(root), "sitecustomize.py"))
	for _, path := range paths {
		allowed := false
		for _, limit := range r.cfg.ResourceLimits.WritePaths {
			if pathutil.IsUnder(path, limit) {
				// The environment is prepared by the Host. Do not follow a
				// process-created reparse point when creating its directories.
				for current := path; pathutil.IsUnder(current, limit); current = filepath.Dir(current) {
					reparse, err := isReparsePoint(current)
					if err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("sandbox: inspect Windows process environment %s: %w", current, err)
					}
					if reparse {
						return fmt.Errorf("sandbox: Windows process environment traverses reparse point: %s", current)
					}
					if filepath.Dir(current) == current {
						break
					}
				}
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("sandbox: Windows process environment escapes explicit writable roots: %s", path)
		}
	}
	return nil
}
