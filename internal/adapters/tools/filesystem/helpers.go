// Package filesystem provides core-native filesystem tools.
package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

const (
	ReadFileToolName      = "read_file"
	ListDirectoryToolName = "list_directory"
	GlobFilesToolName     = "glob_files"
	SearchFilesToolName   = "search_files"
	WriteFileToolName     = "write_file"
	PatchFileToolName     = "patch_file"
)

type constraintAwareFileSystemRuntime interface {
	FileSystemFor(sandbox.Constraints) sandbox.FileSystem
}

func runtimeFileSystem(runtime sandbox.Runtime, meta map[string]any) (sandbox.FileSystem, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tools/filesystem: sandbox runtime is required")
	}
	if provider, ok := runtime.(constraintAwareFileSystemRuntime); ok {
		if fsys := provider.FileSystemFor(constraintsFromMeta(meta)); fsys != nil {
			return fsys, nil
		}
	}
	fsys := runtime.FileSystem()
	if fsys == nil {
		return nil, fmt.Errorf("tools/filesystem: sandbox filesystem is unavailable")
	}
	return fsys, nil
}

func constraintsFromMeta(meta map[string]any) sandbox.Constraints {
	raw, ok := meta["sandbox_constraints"]
	if !ok || raw == nil {
		return sandbox.Constraints{}
	}
	if typed, ok := raw.(sandbox.Constraints); ok {
		return sandbox.NormalizeConstraints(typed)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return sandbox.Constraints{}
	}
	var out sandbox.Constraints
	if err := json.Unmarshal(data, &out); err != nil {
		return sandbox.Constraints{}
	}
	return sandbox.NormalizeConstraints(out)
}

func normalizePath(fsys sandbox.FileSystem, value string) (string, error) {
	if fsys == nil {
		return "", fmt.Errorf("tools/filesystem: filesystem is required")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("tools/filesystem: path is required")
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

func decodeInput(call tool.Call, dst any) error {
	if len(call.Input) == 0 {
		return nil
	}
	if err := json.Unmarshal(call.Input, dst); err != nil {
		return fmt.Errorf("tools/filesystem: invalid json input: %w", err)
	}
	return nil
}

func jsonResult(call tool.Call, name string, payload map[string]any, meta map[string]any) (tool.Result, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		ID:   strings.TrimSpace(call.ID),
		Name: name,
		Content: []model.Part{{
			Kind: model.PartJSON,
			JSON: &model.JSONPart{Value: raw},
		}},
		Meta: filesystemToolMeta(meta),
	}, nil
}

func filesystemToolMeta(values map[string]any) map[string]any {
	out := maps.Clone(values)
	if out == nil {
		out = map[string]any{}
	}
	caelis, _ := out["caelis"].(map[string]any)
	caelis = maps.Clone(caelis)
	if caelis == nil {
		caelis = map[string]any{}
	}
	caelis["version"] = 1
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	runtimeMeta = maps.Clone(runtimeMeta)
	if runtimeMeta == nil {
		runtimeMeta = map[string]any{}
	}
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	toolMeta = maps.Clone(toolMeta)
	if toolMeta == nil {
		toolMeta = map[string]any{}
	}
	for key, value := range values {
		if key == "caelis" || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			toolMeta[key] = text
			continue
		}
		toolMeta[key] = value
	}
	runtimeMeta["tool"] = toolMeta
	caelis["runtime"] = runtimeMeta
	out["caelis"] = caelis
	return out
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func clampLimit(value int, fallback int, max int) int {
	if value <= 0 {
		value = fallback
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

func parseExclude(values []string) []pathExcludeRule {
	if len(values) == 0 {
		return nil
	}
	out := make([]pathExcludeRule, 0, len(values))
	for _, value := range values {
		pattern := normalizeRelativeMatchPath(value)
		if pattern == "" {
			continue
		}
		out = append(out, pathExcludeRule{pattern: pattern})
	}
	return out
}

type pathExcludeRule struct {
	pattern   string
	negated   bool
	recursive bool
	dirOnly   bool
}

func defaultExcludeRules() []pathExcludeRule {
	return []pathExcludeRule{{pattern: ".git", recursive: true, dirOnly: true}}
}

func workspaceExcludeRules(fsys sandbox.FileSystem, root string) []pathExcludeRule {
	rules := gitignoreExcludeRules(fsys, root)
	return append(rules, defaultExcludeRules()...)
}

func shouldExclude(root string, candidate string, isDir bool, rules []pathExcludeRule) bool {
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
	return r.recursive && pathGlobMatch("**/"+pattern, rel)
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

func gitignoreExcludeRules(fsys sandbox.FileSystem, root string) []pathExcludeRule {
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
		pattern = strings.TrimPrefix(pattern, "/")
		dirOnly := strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
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

func pathGlobMatch(pattern string, rel string) bool {
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

func matchPathGlobSegments(patternParts []string, pathParts []string) bool {
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
		root = filepath.Join(append([]string{root}, rootParts...)...)
	}
	if root == "" {
		root = "."
	}
	return filepath.Clean(root), filepath.ToSlash(strings.Join(patternParts, string(filepath.Separator)))
}

func walkDir(fsys sandbox.FileSystem, root string, fn fs.WalkDirFunc) error {
	if fsys == nil {
		return fmt.Errorf("tools/filesystem: filesystem is required")
	}
	return fsys.WalkDir(root, fn)
}
