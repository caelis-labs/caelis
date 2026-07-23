package presets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

func decideFilesystemWrite(input policy.ToolContext, def sandbox.Constraints) (policy.Decision, error) {
	paths, err := candidatePaths(input)
	if err != nil {
		return policyErrorOrDeny(err)
	}
	if err := ensurePathsOutsideDefaultHiddenRoots(paths, approvedOverrideRoots(input.Options), "write"); err != nil {
		return deny(err.Error()), nil
	}
	if err := ensurePathsWithinRoots(paths, writableRoots(input.Options), "write"); err == nil {
		return allow(def), nil
	}
	return askPathWriteApproval(input, def, paths)
}

func askPathWriteApproval(input policy.ToolContext, def sandbox.Constraints, paths []string) (policy.Decision, error) {
	reason := "write outside allowed roots requires approval"
	constraints := withPathWriteGrants(def, paths)
	decision, err := askApproval(reason, constraints, input)
	if err != nil {
		return policy.Decision{}, err
	}
	decision.Metadata = map[string]any{
		"approval_reason": reason,
		"risk_class":      riskClassPathEscape,
	}
	return decision, nil
}

// withPathWriteGrants adds exact target paths as read-write rules so policyfs
// backends can authorize the approved write without granting parent directories.
// The sandbox route is kept so enforcement stays path-scoped rather than full host.
func withPathWriteGrants(base sandbox.Constraints, paths []string) sandbox.Constraints {
	out := sandbox.NormalizeConstraints(base)
	extra := make([]sandbox.PathRule, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		extra = append(extra, sandbox.PathRule{Path: path, Access: sandbox.PathAccessReadWrite})
	}
	out.PathRules = mergePathRules(out.PathRules, extra)
	return out
}

func ensureReadPathsOutsideDefaultHiddenRoots(input policy.ToolContext) error {
	paths, err := candidatePaths(input)
	if err != nil {
		return err
	}
	return ensurePathsOutsideDefaultHiddenRoots(paths, approvedOverrideRoots(input.Options), "read")
}

func ensurePathsWithinRoots(paths []string, roots []string, action string) error {
	for _, one := range paths {
		if strings.TrimSpace(one) == "" {
			continue
		}
		if !withinAnyRoot(one, roots) {
			return fmt.Errorf("%s target %q is outside allowed roots", action, one)
		}
	}
	return nil
}

func writableRoots(opts policy.ModeOptions) []string {
	roots := make([]string, 0, 2+len(opts.ExtraWriteRoots))
	roots = appendNonEmpty(roots, opts.WorkspaceRoot, opts.TempRoot)
	roots = appendNonEmpty(roots, opts.ExtraWriteRoots...)
	return roots
}

func appendNonEmpty(dst []string, values ...string) []string {
	for _, one := range values {
		if trimmed := strings.TrimSpace(one); trimmed != "" {
			dst = append(dst, filepath.Clean(trimmed))
		}
	}
	return dst
}

func approvedOverrideRoots(opts policy.ModeOptions) []string {
	roots := make([]string, 0, len(opts.ExtraReadRoots)+len(opts.ExtraWriteRoots))
	roots = appendNonEmpty(roots, opts.ExtraWriteRoots...)
	roots = appendNonEmpty(roots, opts.ExtraReadRoots...)
	return roots
}

func ensurePathsOutsideDefaultHiddenRoots(paths []string, approvedRoots []string, action string) error {
	for _, one := range paths {
		target := normalizeTarget(one)
		if target == "" {
			continue
		}
		if withinAnyRoot(target, approvedRoots) {
			continue
		}
		if withinAnyRoot(target, defaultHiddenUserRoots()) {
			return fmt.Errorf("%s target %q is under a sensitive user configuration path and is blocked", action, one)
		}
	}
	return nil
}

func defaultHiddenUserRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".kube"),
		filepath.Join(home, ".docker"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, ".npmrc"),
		filepath.Join(home, ".config", "gh"),
		filepath.Join(home, ".config", "gcloud"),
	}
}

func withinAnyRoot(target string, roots []string) bool {
	target = normalizeTarget(target)
	if target == "" {
		return true
	}
	for _, root := range roots {
		root = normalizeTarget(root)
		if root == "" {
			continue
		}
		if target == root || strings.HasPrefix(target, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func normalizeTarget(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(string(filepath.Separator), value)
	}
	return filepath.Clean(value)
}

func candidatePaths(input policy.ToolContext) ([]string, error) {
	args, err := policy.CallArgs(input.Call)
	if err != nil {
		return nil, err
	}
	name := toolName(input)
	info, ok := names.LookupExecutable(name)
	if !ok {
		return nil, nil
	}
	switch info.ResultStyle {
	case names.ResultRead, names.ResultMutation, names.ResultSearch:
		return resolvePathsAgainstWorkspace(stringValues(args["path"]), input.Options.WorkspaceRoot), nil
	case names.ResultGlob:
		return globRoots(stringValues(args["pattern"]), input.Options.WorkspaceRoot), nil
	default:
		return nil, nil
	}
}

func resolvePathsAgainstWorkspace(paths []string, workspaceRoot string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if resolved := resolvePolicyPath(path, workspaceRoot); resolved != "" {
			out = append(out, resolved)
		}
	}
	return out
}

func resolvePolicyPath(value string, workspaceRoot string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	base := strings.TrimSpace(workspaceRoot)
	if base == "" {
		base = string(filepath.Separator)
	}
	return filepath.Clean(filepath.Join(base, value))
}

func globRoots(patterns []string, workspaceRoot string) []string {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		pattern = resolvePolicyPath(pattern, workspaceRoot)
		root := pattern
		for i, r := range pattern {
			if r == '*' || r == '?' || r == '[' {
				root = pattern[:i]
				break
			}
		}
		root = strings.TrimRight(root, string(filepath.Separator))
		if root == "" {
			root = string(filepath.Separator)
		}
		out = append(out, root)
	}
	return out
}

func stringValues(value any) []string {
	switch typed := value.(type) {
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}
		}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, stringValues(item)...)
		}
		return out
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	}
	return nil
}
