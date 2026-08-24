package policy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

type Type string

const (
	TypeReadOnly       Type = "read_only"
	TypeWorkspaceWrite Type = "workspace_write"
	TypeDangerFull     Type = "danger_full_access"
	TypeExternal       Type = "external_sandbox"
)

type Policy struct {
	Type          Type
	NetworkAccess bool
	WritableRoots []string
	// GitProtectionRoots limits bounded metadata discovery to app-provided
	// authorities rather than temporary and developer-cache write grants.
	GitProtectionRoots []string
	// WriteOverrides are request-scoped grants used to reopen one matching
	// protected path without disabling the semantic protection globally.
	WriteOverrides   []string
	HiddenRoots      []string
	ReadOnlySubpaths []string
}

func Default(cfg sandbox.Config, constraints sandbox.Constraints) Policy {
	p := Policy{
		Type:               TypeWorkspaceWrite,
		NetworkAccess:      constraints.Network != sandbox.NetworkDisabled,
		WritableRoots:      append([]string(nil), cfg.WritableRoots...),
		GitProtectionRoots: append([]string(nil), cfg.WritableRoots...),
		ReadOnlySubpaths:   append([]string(nil), cfg.ReadOnlySubpaths...),
	}
	switch constraints.Permission {
	case sandbox.PermissionFullAccess:
		p.Type = TypeDangerFull
		p.NetworkAccess = true
		p.WritableRoots = nil
		p.GitProtectionRoots = nil
		p.WriteOverrides = nil
		p.HiddenRoots = nil
		p.ReadOnlySubpaths = nil
	default:
		p.ReadOnlySubpaths = append(p.ReadOnlySubpaths, ".git")
		if constraints.Network == "" {
			p.NetworkAccess = true
		}
		applyPathRules(&p, constraints.PathRules)
	}
	p.WritableRoots = normalizeStringList(p.WritableRoots)
	p.GitProtectionRoots = normalizeStringList(p.GitProtectionRoots)
	p.WriteOverrides = normalizeStringList(p.WriteOverrides)
	p.HiddenRoots = normalizeStringList(p.HiddenRoots)
	p.ReadOnlySubpaths = normalizeStringList(p.ReadOnlySubpaths)
	return p
}

func pathIsUnder(target, root string) bool {
	target = filepath.Clean(target)
	root = filepath.Clean(root)
	if runtime.GOOS == "windows" {
		target = strings.ToLower(target)
		root = strings.ToLower(root)
	}
	if target == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(target, root)
}

func applyPathRules(p *Policy, rules []sandbox.PathRule) {
	if p == nil || len(rules) == 0 {
		return
	}
	for _, rule := range rules {
		path := strings.TrimSpace(rule.Path)
		if path == "" {
			continue
		}
		switch rule.Access {
		case sandbox.PathAccessReadWrite:
			p.WritableRoots = append(p.WritableRoots, path)
			p.WriteOverrides = append(p.WriteOverrides, path)
		case sandbox.PathAccessHidden:
			p.HiddenRoots = append(p.HiddenRoots, path)
		}
	}
}

func SandboxPathVariants(path string) []string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return nil
	}
	variants := []string{cleaned}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && strings.TrimSpace(resolved) != "" {
		variants = append(variants, filepath.Clean(resolved))
	}
	return normalizeStringList(variants)
}

func WritableRootPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	// Writable roots are authority boundaries. Do not broaden a missing cache
	// directory such as ~/.pnpm-store to its existing parent, because that can
	// turn a narrow developer-cache grant into a $HOME write grant.
	return filepath.Clean(path)
}

func ResolveSandboxPath(baseDir, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	if strings.TrimSpace(baseDir) == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(baseDir, trimmed))
}

func FilterExistingPaths(values []string) []string {
	return filterExistingPaths(values)
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filterExistingPaths(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range normalizeStringList(values) {
		if _, err := os.Stat(value); err == nil {
			out = append(out, value)
		}
	}
	return out
}
