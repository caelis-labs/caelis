package sandboxpolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	policyapi "github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
)

// EffectiveConfig resolves the product-owned writable roots for one workspace.
// The workspace remains the command CWD even when it is too broad to grant as
// a write authority boundary.
func EffectiveConfig(workspace string, cfg configstore.SandboxConfig) configstore.SandboxConfig {
	home, _ := os.UserHomeDir()
	return effectiveConfig(workspace, home, cfg)
}

func effectiveConfig(workspace, home string, cfg configstore.SandboxConfig) configstore.SandboxConfig {
	cfg = configstore.DefaultSandboxConfig(cfg)
	roots := make([]string, 0, 1+len(cfg.WritableRoots))
	seen := map[string]struct{}{}
	appendRoot := func(value string) {
		root := resolveRoot(workspace, value)
		if root == "" || broadWritableRoot(root, home) {
			return
		}
		key := canonicalRoot(root)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		roots = append(roots, root)
	}
	appendRoot(workspace)
	for _, root := range cfg.WritableRoots {
		appendRoot(root)
	}
	cfg.WritableRoots = roots
	return cfg
}

func resolveRoot(workspace, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !filepath.IsAbs(value) {
		workspace = strings.TrimSpace(workspace)
		if workspace == "" {
			return ""
		}
		value = filepath.Join(workspace, value)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

func broadWritableRoot(root, home string) bool {
	root = canonicalRoot(resolveRoot("", root))
	if root == "" || filepath.Dir(root) == root {
		return true
	}
	home = canonicalRoot(resolveRoot("", home))
	return home != "" && pathContains(root, home)
}

func canonicalRoot(root string) string {
	root = filepath.Clean(root)
	current := root
	missing := []string{}
	for current != "" {
		if resolved, err := filepath.EvalSymlinks(current); err == nil && strings.TrimSpace(resolved) != "" {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	return root
}

func pathContains(root, target string) bool {
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func NormalizeBackend(backend string) (string, error) {
	switch normalized := sandbox.CanonicalBackend(sandbox.Backend(backend)); normalized {
	case "":
		return "auto", nil
	case sandbox.BackendHost, sandbox.BackendSeatbelt, sandbox.BackendBwrap, sandbox.BackendWindows:
		return string(normalized), nil
	case sandbox.BackendLandlock:
		return "", fmt.Errorf("gatewayapp: sandbox backend %q is retired from the product; use auto (bwrap on Linux) or explicit host execution", backend)
	default:
		return "", fmt.Errorf("gatewayapp: unknown sandbox backend %q", backend)
	}
}

func MergeConfig(stored configstore.SandboxConfig, override configstore.SandboxConfig) configstore.SandboxConfig {
	overrideNetworkSet := override.NetworkEnabled != nil
	stored = configstore.NormalizeSandboxConfig(stored)
	override = configstore.NormalizeSandboxConfig(override)
	if override.RequestedType != "" {
		stored.RequestedType = override.RequestedType
	}
	if override.HelperPath != "" {
		stored.HelperPath = override.HelperPath
	}
	if len(override.WritableRoots) > 0 {
		stored.WritableRoots = append([]string(nil), override.WritableRoots...)
	}
	if len(override.ReadOnlySubpaths) > 0 {
		stored.ReadOnlySubpaths = append([]string(nil), override.ReadOnlySubpaths...)
	}
	if overrideNetworkSet {
		value := *override.NetworkEnabled
		stored.NetworkEnabled = &value
	}
	if stored.RequestedType == "" {
		stored.RequestedType = "auto"
	}
	return configstore.DefaultSandboxConfig(stored)
}

func WithPolicyMetadata(metadata map[string]any, cfg configstore.SandboxConfig) map[string]any {
	out := cloneMap(metadata)
	if out == nil {
		out = map[string]any{}
	}
	cfg = configstore.DefaultSandboxConfig(cfg)
	if len(cfg.WritableRoots) > 0 {
		out[policyapi.MetadataWritableRoots] = mergePolicyWriteRoots(out[policyapi.MetadataWritableRoots], cfg.WritableRoots)
	}
	out["policy_network_enabled"] = configstore.SandboxNetworkEnabled(cfg)
	return out
}

func mergePolicyWriteRoots(existing any, values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	appendOne := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	switch typed := existing.(type) {
	case []string:
		for _, one := range typed {
			appendOne(one)
		}
	case []any:
		for _, one := range typed {
			text, _ := one.(string)
			appendOne(text)
		}
	}
	for _, one := range values {
		appendOne(one)
	}
	return out
}

func cloneMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
