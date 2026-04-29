package policy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
)

type Type string

const (
	TypeReadOnly       Type = "read_only"
	TypeWorkspaceWrite Type = "workspace_write"
	TypeDangerFull     Type = "danger_full_access"
	TypeExternal       Type = "external_sandbox"
)

type Policy struct {
	Type             Type
	NetworkAccess    bool
	ReadableRoots    []string
	WritableRoots    []string
	ReadOnlySubpaths []string
}

func Default(cfg sdksandbox.Config, constraints sdksandbox.Constraints) Policy {
	p := Policy{
		Type:             TypeWorkspaceWrite,
		NetworkAccess:    constraints.Network != sdksandbox.NetworkDisabled,
		ReadableRoots:    append([]string(nil), cfg.ReadableRoots...),
		WritableRoots:    append([]string(nil), cfg.WritableRoots...),
		ReadOnlySubpaths: append([]string(nil), cfg.ReadOnlySubpaths...),
	}
	switch constraints.Permission {
	case sdksandbox.PermissionFullAccess:
		p.Type = TypeDangerFull
		p.NetworkAccess = true
		p.ReadableRoots = nil
		p.WritableRoots = nil
		p.ReadOnlySubpaths = nil
	default:
		if len(p.WritableRoots) == 0 {
			p.WritableRoots = []string{"."}
		}
		if len(p.ReadOnlySubpaths) == 0 {
			p.ReadOnlySubpaths = []string{".git"}
		}
		if constraints.Network == "" {
			p.NetworkAccess = true
		}
		applyPathRules(&p, constraints.PathRules)
	}
	p.ReadableRoots = normalizeStringList(p.ReadableRoots)
	p.WritableRoots = normalizeStringList(p.WritableRoots)
	p.ReadOnlySubpaths = normalizeStringList(p.ReadOnlySubpaths)
	return p
}

func applyPathRules(p *Policy, rules []sdksandbox.PathRule) {
	if p == nil || len(rules) == 0 {
		return
	}
	for _, rule := range rules {
		path := strings.TrimSpace(rule.Path)
		if path == "" {
			continue
		}
		switch rule.Access {
		case sdksandbox.PathAccessReadWrite:
			p.WritableRoots = append(p.WritableRoots, path)
		case sdksandbox.PathAccessReadOnly:
			p.ReadableRoots = append(p.ReadableRoots, path)
		}
	}
}

func HasExplicitReadableRoots(p Policy) bool {
	return len(normalizeStringList(p.ReadableRoots)) > 0
}

func ShellReadableRoots(p Policy, workDir string) []string {
	if !HasExplicitReadableRoots(p) {
		return nil
	}
	roots := make([]string, 0, len(p.ReadableRoots)+len(p.WritableRoots)+16)
	for _, one := range p.ReadableRoots {
		if resolved := ResolveSandboxPath(workDir, one); resolved != "" {
			roots = append(roots, SandboxPathVariants(resolved)...)
		}
	}
	for _, one := range p.WritableRoots {
		if resolved := ResolveSandboxPath(workDir, one); resolved != "" {
			roots = append(roots, SandboxPathVariants(resolved)...)
		}
	}
	roots = append(roots, ScratchReadableRoots()...)
	roots = append(roots, PlatformReadableRoots(runtime.GOOS)...)
	return normalizeStringList(filterExistingPaths(roots))
}

func ScratchReadableRoots() []string {
	roots := []string{"/tmp", "/var/tmp", "/private/tmp"}
	if tmp := strings.TrimSpace(os.TempDir()); tmp != "" {
		roots = append(roots, tmp)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		roots = append(roots, filepath.Join(home, ".cache"))
		roots = append(roots, filepath.Join(home, "Library", "Caches"))
	}
	return normalizeStringList(expandSandboxPathVariants(roots))
}

func PlatformReadableRoots(goos string) []string {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "darwin":
		return normalizeStringList(expandSandboxPathVariants([]string{
			"/System", "/usr", "/bin", "/sbin", "/Library", "/Applications", "/opt", "/private/etc", "/private/var/db/timezone", "/dev",
		}))
	default:
		return normalizeStringList(expandSandboxPathVariants([]string{
			"/bin", "/usr", "/lib", "/lib64", "/etc", "/dev", "/proc", "/sys", "/run", "/var", "/opt",
		}))
	}
}

func expandSandboxPathVariants(paths []string) []string {
	values := make([]string, 0, len(paths)*2)
	for _, one := range paths {
		values = append(values, SandboxPathVariants(one)...)
	}
	return values
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
