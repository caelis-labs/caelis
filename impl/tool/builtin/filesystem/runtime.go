package filesystem

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

type constraintAwareFileSystemRuntime interface {
	FileSystemFor(sandbox.Constraints) sandbox.FileSystem
}

func runtimeOrDefault(runtime sandbox.Runtime) (sandbox.Runtime, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tool: sandbox runtime is required")
	}
	return runtime, nil
}

func fileSystemFromRuntime(runtime sandbox.Runtime, meta map[string]any) sandbox.FileSystem {
	if runtime == nil {
		return nil
	}
	constraints := constraintsFromMetadata(meta)
	if provider, ok := runtime.(constraintAwareFileSystemRuntime); ok {
		if fsys := provider.FileSystemFor(constraints); fsys != nil {
			return fsys
		}
	}
	return runtime.FileSystem()
}

func constraintsFromMetadata(meta map[string]any) sandbox.Constraints {
	if meta == nil {
		return sandbox.Constraints{}
	}
	raw, ok := meta["sandbox_constraints"]
	if !ok || raw == nil {
		return sandbox.Constraints{}
	}
	if typed, ok := raw.(sandbox.Constraints); ok {
		return sandbox.NormalizeConstraints(typed)
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return sandbox.Constraints{}
	}
	var out sandbox.Constraints
	if err := json.Unmarshal(bytes, &out); err != nil {
		return sandbox.Constraints{}
	}
	return sandbox.NormalizeConstraints(out)
}

func normalizePathWithFS(fsys sandbox.FileSystem, value string) (string, error) {
	if fsys == nil {
		return "", fmt.Errorf("tool: filesystem is required")
	}
	if value == "" {
		return "", fmt.Errorf("tool: empty path")
	}
	if strings.HasPrefix(value, "~/") {
		home, err := fsys.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, value[2:])
	}
	if !filepath.IsAbs(value) {
		cwd, err := fsys.Getwd()
		if err != nil {
			return "", err
		}
		value = filepath.Join(cwd, value)
	}
	return filepath.Clean(value), nil
}

func walkDir(fsys sandbox.FileSystem, root string, fn fs.WalkDirFunc) error {
	if fsys == nil {
		return fmt.Errorf("tool: filesystem is required")
	}
	return fsys.WalkDir(root, fn)
}

func parseStringSliceArg(args map[string]any, key string) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	switch typed := raw.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if item != "" {
				out = append(out, item)
			}
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("tool: arg %q must be an array of strings", key)
			}
			if text != "" {
				out = append(out, text)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("tool: arg %q must be an array of strings", key)
	}
}

type pathMatchRule struct {
	pattern   string
	negated   bool
	recursive bool
	dirOnly   bool
}

type pathExcludeRule = pathMatchRule

func pathRulesFromPatterns(patterns []string) []pathMatchRule {
	if len(patterns) == 0 {
		return nil
	}
	rules := make([]pathMatchRule, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = normalizeRelativeMatchPath(pattern)
		if pattern == "" {
			continue
		}
		rules = append(rules, pathMatchRule{
			pattern:   pattern,
			recursive: !strings.Contains(pattern, "/"),
		})
	}
	return rules
}

func excludeRulesFromPatterns(patterns []string) []pathExcludeRule {
	return pathRulesFromPatterns(patterns)
}

func defaultWorkspaceExcludeRules() []pathExcludeRule {
	return []pathExcludeRule{
		{pattern: ".git", recursive: true, dirOnly: true},
	}
}

func workspaceExcludeRules(fsys sandbox.FileSystem, root string) []pathExcludeRule {
	rules := gitignoreExcludePatterns(fsys, root)
	rules = append(rules, defaultWorkspaceExcludeRules()...)
	return rules
}

func shouldExcludePath(root, candidate string, isDir bool, rules []pathExcludeRule) bool {
	if len(rules) == 0 {
		return false
	}
	rel := relativeMatchPath(root, candidate)
	excluded := false
	for _, rule := range rules {
		if rule.matches(rel, isDir) {
			excluded = !rule.negated
		}
	}
	return excluded
}

func shouldIncludeFilePath(root, candidate string, rules []pathMatchRule) bool {
	if len(rules) == 0 {
		return true
	}
	return pathMatchesAnyRule(relativeMatchPath(root, candidate), false, rules)
}

func pathMatchesAnyRule(rel string, isDir bool, rules []pathMatchRule) bool {
	for _, rule := range rules {
		if rule.matches(rel, isDir) {
			return true
		}
	}
	return false
}

func relativeMatchPath(root, candidate string) string {
	rel := candidate
	if root != "" {
		if computed, err := filepath.Rel(root, candidate); err == nil {
			rel = computed
		}
	}
	return normalizeRelativeMatchPath(rel)
}

func (r pathMatchRule) matches(rel string, isDir bool) bool {
	pattern := normalizeRelativeMatchPath(r.pattern)
	if pattern == "" {
		return false
	}
	if r.dirOnly {
		if pathGlobMatch(pattern, rel) || (r.recursive && pathGlobMatch("**/"+pattern, rel)) {
			return true
		}
		if strings.HasPrefix(rel, pattern+"/") || (r.recursive && pathHasMatchingDirSegment(rel, pattern)) {
			return true
		}
		return isDir && rel == pattern
	}
	if pathGlobMatch(pattern, rel) {
		return true
	}
	if r.recursive && pathGlobMatch("**/"+pattern, rel) {
		return true
	}
	return false
}

func pathHasMatchingDirSegment(rel string, pattern string) bool {
	parts := splitPathSegments(rel)
	if len(parts) < 2 {
		return false
	}
	for i := 0; i < len(parts)-1; i++ {
		if matched, err := path.Match(pattern, parts[i]); err == nil && matched {
			return true
		}
	}
	return false
}

func gitignoreExcludePatterns(fsys sandbox.FileSystem, root string) []pathExcludeRule {
	if fsys == nil || strings.TrimSpace(root) == "" {
		return nil
	}
	raw, err := fsys.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n"), "\n")
	rules := make([]pathExcludeRule, 0, len(lines))
	for _, line := range lines {
		pattern := strings.TrimSpace(line)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		negated := strings.HasPrefix(pattern, "!")
		if negated {
			pattern = strings.TrimSpace(strings.TrimPrefix(pattern, "!"))
		}
		anchored := strings.HasPrefix(pattern, "/")
		if anchored {
			pattern = strings.TrimPrefix(pattern, "/")
		}
		dirOnly := strings.HasSuffix(pattern, "/")
		if dirOnly {
			pattern = strings.TrimSuffix(pattern, "/")
		}
		pattern = normalizeRelativeMatchPath(pattern)
		if pattern == "" {
			continue
		}
		rules = append(rules, pathExcludeRule{
			pattern:   pattern,
			negated:   negated,
			recursive: !anchored && !strings.Contains(pattern, "/"),
			dirOnly:   dirOnly,
		})
	}
	return rules
}

func normalizeRelativeMatchPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = filepath.ToSlash(filepath.Clean(value))
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	if value == "." {
		return ""
	}
	return value
}

func hasPathGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[") || hasPathBraceExpansion(pattern)
}

func pathGlobMatch(pattern, rel string) bool {
	pattern = normalizeRelativeMatchPath(pattern)
	rel = normalizeRelativeMatchPath(rel)
	if pattern == "" {
		return rel == ""
	}
	relParts := splitPathSegments(rel)
	for _, expanded := range expandPathBraceAlternates(pattern) {
		if matchPathGlobSegments(splitPathSegments(expanded), relParts) {
			return true
		}
	}
	return false
}

const maxPathBraceAlternates = 128

func hasPathBraceExpansion(pattern string) bool {
	_, _, _, ok := firstPathBraceExpansion(pattern)
	return ok
}

func expandPathBraceAlternates(pattern string) []string {
	return expandPathBraceAlternatesLimited(pattern, maxPathBraceAlternates)
}

func expandPathBraceAlternatesLimited(pattern string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	start, end, parts, ok := firstPathBraceExpansion(pattern)
	if !ok {
		return []string{pattern}
	}
	out := make([]string, 0, len(parts))
	prefix := pattern[:start]
	suffix := pattern[end+1:]
	for _, part := range parts {
		for _, expanded := range expandPathBraceAlternatesLimited(prefix+part+suffix, limit-len(out)) {
			out = append(out, expanded)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func firstPathBraceExpansion(pattern string) (int, int, []string, bool) {
	depth := 0
	start := -1
	partStart := 0
	sawComma := false
	inClass := false
	escaped := false
	parts := []string{}
	for i := 0; i < len(pattern); i++ {
		switch ch := pattern[i]; {
		case escaped:
			escaped = false
		case ch == '\\':
			escaped = true
		case inClass:
			if ch == ']' {
				inClass = false
			}
		case ch == '[':
			inClass = true
		case ch == '{':
			if depth == 0 {
				start = i
				partStart = i + 1
				sawComma = false
				parts = parts[:0]
			}
			depth++
		case ch == '}' && depth > 0:
			depth--
			if depth == 0 {
				if !sawComma {
					start = -1
					continue
				}
				parts = append(parts, pattern[partStart:i])
				return start, i, parts, true
			}
		case ch == ',' && depth == 1:
			sawComma = true
			parts = append(parts, pattern[partStart:i])
			partStart = i + 1
		}
	}
	return 0, 0, nil, false
}

func splitPathSegments(value string) []string {
	value = normalizeRelativeMatchPath(value)
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func matchPathGlobSegments(patternParts, pathParts []string) bool {
	if len(patternParts) == 0 {
		return len(pathParts) == 0
	}
	if patternParts[0] == "**" {
		if matchPathGlobSegments(patternParts[1:], pathParts) {
			return true
		}
		for i := 0; i < len(pathParts); i++ {
			if matchPathGlobSegments(patternParts[1:], pathParts[i+1:]) {
				return true
			}
		}
		return false
	}
	if len(pathParts) == 0 {
		return false
	}
	matched, err := path.Match(patternParts[0], pathParts[0])
	if err != nil || !matched {
		return false
	}
	return matchPathGlobSegments(patternParts[1:], pathParts[1:])
}

func splitAbsoluteGlobPattern(pattern string) (string, string) {
	pattern = filepath.Clean(pattern)
	volume := filepath.VolumeName(pattern)
	rest := strings.TrimPrefix(pattern, volume)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	segments := strings.FieldsFunc(rest, func(r rune) bool { return r == filepath.Separator })
	metaIndex := len(segments)
	for i, segment := range segments {
		if hasPathGlobMeta(segment) {
			metaIndex = i
			break
		}
	}
	rootParts := segments[:metaIndex]
	patternParts := segments[metaIndex:]
	root := volume
	if filepath.IsAbs(pattern) {
		if root == "" {
			root = string(filepath.Separator)
		} else if !strings.HasSuffix(root, string(filepath.Separator)) {
			root += string(filepath.Separator)
		}
	}
	if len(rootParts) > 0 {
		pieces := append([]string{root}, rootParts...)
		root = filepath.Join(pieces...)
	}
	if root == "" {
		root = "."
	}
	return filepath.Clean(root), filepath.ToSlash(strings.Join(patternParts, string(filepath.Separator)))
}
