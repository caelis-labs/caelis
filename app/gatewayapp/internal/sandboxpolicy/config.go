package sandboxpolicy

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
)

func NormalizeBackend(backend string) (string, error) {
	switch normalized := sandbox.CanonicalBackend(sandbox.Backend(backend)); normalized {
	case "":
		return "auto", nil
	case sandbox.BackendHost, sandbox.BackendSeatbelt, sandbox.BackendBwrap, sandbox.BackendLandlock, sandbox.BackendWindows:
		return string(normalized), nil
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
	if len(override.ReadableRoots) > 0 {
		stored.ReadableRoots = append([]string(nil), override.ReadableRoots...)
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

func WithPolicyRootMetadata(
	metadata map[string]any,
	cfg configstore.SandboxConfig,
	workspaceDir string,
	skillMetas []skill.Meta,
) map[string]any {
	out := cloneMap(metadata)
	if out == nil {
		out = map[string]any{}
	}
	cfg = configstore.DefaultSandboxConfig(cfg)
	skillReadRoots := ExternalSkillReadRoots(workspaceDir, skillMetas)
	readRoots := configstore.DedupeStrings(append(append([]string(nil), cfg.ReadableRoots...), skillReadRoots...))
	if len(readRoots) > 0 {
		out["policy_extra_read_roots"] = mergePolicyRootMetadata(out["policy_extra_read_roots"], readRoots)
	}
	if len(cfg.WritableRoots) > 0 {
		out["policy_extra_write_roots"] = mergePolicyRootMetadata(out["policy_extra_write_roots"], cfg.WritableRoots)
	}
	out["policy_network_enabled"] = configstore.SandboxNetworkEnabled(cfg)
	return out
}

func mergePolicyRootMetadata(existing any, values []string) []string {
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
