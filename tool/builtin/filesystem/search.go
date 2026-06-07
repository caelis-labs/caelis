package filesystem

import (
	"bufio"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/tool"
)

// searchFiles implements the SEARCH tool with recursive directory walk.
type searchFiles struct{}

func (*searchFiles) Definition() tool.Definition {
	return tool.Definition{
		Name:        "SEARCH",
		Description: "Search for text in files. Recursively walks directories.",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"query":          {Type: "string", Description: "Text to search for"},
				"path":           {Type: "string", Description: "Path to search in (file or directory)"},
				"include":        {Type: "string", Description: "File pattern to include (e.g. '*.go')"},
				"exclude":        {Type: "array", Items: &tool.Schema{Type: "string"}, Description: "Patterns to exclude"},
				"case_sensitive": {Type: "boolean", Description: "Case sensitive search (default: false)"},
				"limit":          {Type: "integer", Description: "Max matches (default: 50)"},
			},
			Required: []string{"query", "path"},
		},
	}
}

func (*searchFiles) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	query, _ := call.Args["query"].(string)
	path, _ := call.Args["path"].(string)
	if query == "" || path == "" {
		return tool.Result{Output: "query and path are required", IsError: true}, nil
	}

	include, _ := call.Args["include"].(string)
	caseSensitive, _ := call.Args["case_sensitive"].(bool)
	limit := 50
	if v, ok := call.Args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	fs := ctx.FileSystem()
	if fs == nil {
		return tool.Result{Output: "sandbox filesystem not available", IsError: true}, nil
	}

	// Check if path is a file or directory.
	info, err := fs.Stat(path)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("path error: %v", err), IsError: true}, nil
	}

	var matches []string
	if info.IsDir {
		matches = searchDir(fs, path, query, include, caseSensitive, limit)
	} else {
		matches = searchInFile(fs, path, query, caseSensitive)
	}

	if len(matches) == 0 {
		return tool.Result{Output: "no matches found"}, nil
	}

	output := strings.Join(matches, "\n")
	if len(matches) >= limit {
		output += fmt.Sprintf("\n... (truncated at %d matches)", limit)
	}

	return tool.Result{Output: output}, nil
}

func searchDir(fs sandbox.FileSystem, dir, query, include string, caseSensitive bool, limit int) []string {
	var matches []string
	entries, err := fs.List(dir)
	if err != nil {
		return nil
	}
	for _, name := range entries {
		if len(matches) >= limit {
			break
		}
		fullPath := dir + "/" + name
		info, err := fs.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir {
			// Skip hidden directories and common exclusions.
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				continue
			}
			matches = append(matches, searchDir(fs, fullPath, query, include, caseSensitive, limit-len(matches))...)
			continue
		}
		if include != "" {
			matched, _ := filepath.Match(include, name)
			if !matched {
				continue
			}
		}
		matches = append(matches, searchInFile(fs, fullPath, query, caseSensitive)...)
	}
	return matches
}

func searchInFile(fs sandbox.FileSystem, fpath string, query string, caseSensitive bool) []string {
	data, err := fs.Read(fpath)
	if err != nil {
		return nil
	}

	var results []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNum := 0
	searchQuery := query
	if !caseSensitive {
		searchQuery = strings.ToLower(query)
	}

	for scanner.Scan() {
		lineNum++
		text := scanner.Text()
		candidate := text
		if !caseSensitive {
			candidate = strings.ToLower(text)
		}
		if strings.Contains(candidate, searchQuery) {
			results = append(results, fmt.Sprintf("%s:%d: %s", fpath, lineNum, text))
		}
	}
	return results
}
