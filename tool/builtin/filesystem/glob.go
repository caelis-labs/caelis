package filesystem

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/tool"
)

// globFiles implements the GLOB tool with recursive ** support.
type globFiles struct{}

func (*globFiles) Definition() tool.Definition {
	return tool.Definition{
		Name:        "GLOB",
		Description: "Find files matching a glob pattern. Supports ** for recursive matching.",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"pattern": {Type: "string", Description: "Glob pattern (e.g. '**/*.go', 'src/*.js')"},
				"root":    {Type: "string", Description: "Root directory (default: '.')"},
				"exclude": {Type: "array", Items: &tool.Schema{Type: "string"}, Description: "Patterns to exclude"},
				"limit":   {Type: "integer", Description: "Max results (default: 200)"},
			},
			Required: []string{"pattern"},
		},
	}
}

func (*globFiles) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	pattern, _ := call.Args["pattern"].(string)
	root, _ := call.Args["root"].(string)
	if pattern == "" {
		return tool.Result{Output: "pattern is required", IsError: true}, nil
	}
	if root == "" {
		root = "."
	}

	fs := ctx.FileSystem()
	if fs == nil {
		return tool.Result{Output: "sandbox filesystem not available", IsError: true}, nil
	}

	var excludes []string
	if raw, ok := call.Args["exclude"].([]any); ok {
		for _, e := range raw {
			if s, ok := e.(string); ok {
				excludes = append(excludes, s)
			}
		}
	}
	excludes = append(excludes, ".git", "node_modules", ".DS_Store")

	limit := 200
	if v, ok := call.Args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	// If pattern has no directory separator and no **, match against root only.
	if !strings.Contains(pattern, "/") && !strings.Contains(pattern, "**") {
		entries, err := fs.List(root)
		if err != nil {
			return tool.Result{Output: fmt.Sprintf("list error: %v", err), IsError: true}, nil
		}
		var matches []string
		for _, name := range entries {
			if shouldExclude(name, excludes) {
				continue
			}
			matched, _ := filepath.Match(pattern, name)
			if matched {
				matches = append(matches, filepath.Join(root, name))
			}
		}
		return globResult(matches, limit), nil
	}

	// Determine the walk root and the effective pattern.
	// For "src/*.go", walk "src/" and match "*.go" inside.
	// For "**/*.go", walk root and match "*.go" at each level.
	// For "src/**/*.go", walk "src/" and match "*.go" at each level.
	walkRoot, effectivePattern := splitGlobPattern(root, pattern)

	var matches []string
	globWalk(fs, walkRoot, root, effectivePattern, excludes, &matches, limit)
	return globResult(matches, limit), nil
}

// splitGlobPattern splits a pattern into a walk root and an effective pattern.
// e.g. ("root", "src/*.go") → ("root/src", "*.go")
// e.g. ("root", "**/*.go") → ("root", "*.go")
// e.g. ("root", "src/**/*.go") → ("root/src", "*.go")
func splitGlobPattern(root, pattern string) (walkRoot string, effectivePattern string) {
	if strings.Contains(pattern, "**") {
		// Split at ** to get prefix and suffix.
		idx := strings.Index(pattern, "**")
		prefix := strings.TrimSuffix(pattern[:idx], "/")
		suffix := strings.TrimPrefix(pattern[idx+2:], "/")
		if prefix != "" {
			walkRoot = filepath.Join(root, prefix)
		} else {
			walkRoot = root
		}
		if suffix == "" {
			suffix = "*"
		}
		return walkRoot, suffix
	}

	// No ** — split at last directory separator.
	// e.g. "src/*.go" → walk "src/", match "*.go"
	// e.g. "*.go" → walk ".", match "*.go"
	dir := filepath.Dir(pattern)
	base := filepath.Base(pattern)
	if dir == "." {
		return root, base
	}
	return filepath.Join(root, dir), base
}

// globWalk recursively walks the filesystem using the sandbox FS.
func globWalk(fs sandbox.FileSystem, dir, root, pattern string, excludes []string, matches *[]string, limit int) {
	if len(*matches) >= limit {
		return
	}
	entries, err := fs.List(dir)
	if err != nil {
		return
	}
	for _, name := range entries {
		if len(*matches) >= limit {
			return
		}
		fullPath := dir + "/" + name
		if shouldExclude(name, excludes) {
			continue
		}
		info, err := fs.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir {
			// Always recurse — pattern matching happens at file level.
			globWalk(fs, fullPath, root, pattern, excludes, matches, limit)
			continue
		}
		// Match file against effective pattern.
		if matchGlobPattern(pattern, name) {
			*matches = append(*matches, fullPath)
		}
	}
}

// matchGlobPattern matches a filename against a glob pattern.
// Supports *, ?, and character classes.
func matchGlobPattern(pattern, name string) bool {
	matched, _ := filepath.Match(pattern, name)
	return matched
}

func globResult(matches []string, limit int) tool.Result {
	if len(matches) == 0 {
		return tool.Result{Output: "no matches found"}
	}
	output := strings.Join(matches, "\n")
	if len(matches) >= limit {
		output += fmt.Sprintf("\n... (truncated at %d results)", limit)
	}
	return tool.Result{Output: output}
}

func shouldExclude(name string, excludes []string) bool {
	for _, ex := range excludes {
		if matched, _ := filepath.Match(ex, name); matched {
			return true
		}
	}
	return false
}
