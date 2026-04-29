package filesystem

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
)

type constraintAwareFileSystemRuntime interface {
	FileSystemFor(sdksandbox.Constraints) sdksandbox.FileSystem
}

func runtimeOrDefault(runtime sdksandbox.Runtime) (sdksandbox.Runtime, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tool: sandbox runtime is required")
	}
	return runtime, nil
}

func fileSystemFromRuntime(runtime sdksandbox.Runtime, meta map[string]any) sdksandbox.FileSystem {
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

func constraintsFromMetadata(meta map[string]any) sdksandbox.Constraints {
	if meta == nil {
		return sdksandbox.Constraints{}
	}
	raw, ok := meta["sandbox_constraints"]
	if !ok || raw == nil {
		return sdksandbox.Constraints{}
	}
	if typed, ok := raw.(sdksandbox.Constraints); ok {
		return sdksandbox.NormalizeConstraints(typed)
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return sdksandbox.Constraints{}
	}
	var out sdksandbox.Constraints
	if err := json.Unmarshal(bytes, &out); err != nil {
		return sdksandbox.Constraints{}
	}
	return sdksandbox.NormalizeConstraints(out)
}

func normalizePathWithFS(fsys sdksandbox.FileSystem, value string) (string, error) {
	if fsys == nil {
		return "", fmt.Errorf("tool: filesystem is required")
	}
	value = strings.TrimSpace(value)
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

func walkDir(fsys sdksandbox.FileSystem, root string, fn fs.WalkDirFunc) error {
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
			if item = strings.TrimSpace(item); item != "" {
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
			if text = strings.TrimSpace(text); text != "" {
				out = append(out, text)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("tool: arg %q must be an array of strings", key)
	}
}

type pathExcludeRule struct {
	pattern   string
	negated   bool
	recursive bool
	dirOnly   bool
}

func excludeRulesFromPatterns(patterns []string) []pathExcludeRule {
	if len(patterns) == 0 {
		return nil
	}
	rules := make([]pathExcludeRule, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = normalizeRelativeMatchPath(pattern)
		if pattern == "" {
			continue
		}
		rules = append(rules, pathExcludeRule{pattern: pattern})
	}
	return rules
}

func shouldExcludePath(root, candidate string, isDir bool, rules []pathExcludeRule) bool {
	if len(rules) == 0 {
		return false
	}
	rel := candidate
	if root != "" {
		if computed, err := filepath.Rel(root, candidate); err == nil {
			rel = computed
		}
	}
	rel = normalizeRelativeMatchPath(rel)
	excluded := false
	for _, rule := range rules {
		if rule.matches(rel, isDir) {
			excluded = !rule.negated
		}
	}
	return excluded
}

func (r pathExcludeRule) matches(rel string, isDir bool) bool {
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

func gitignoreExcludePatterns(fsys sdksandbox.FileSystem, root string) []pathExcludeRule {
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
	return strings.ContainsAny(pattern, "*?[")
}

func pathGlobMatch(pattern, rel string) bool {
	pattern = normalizeRelativeMatchPath(pattern)
	rel = normalizeRelativeMatchPath(rel)
	if pattern == "" {
		return rel == ""
	}
	return matchPathGlobSegments(splitPathSegments(pattern), splitPathSegments(rel))
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
