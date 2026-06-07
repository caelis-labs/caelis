package filesystem

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/tool"
)

// readFile implements the READ tool.
type readFile struct{}

func (*readFile) Definition() tool.Definition {
	return tool.Definition{
		Name:        "READ",
		Description: "Read the contents of a file at the given path.",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"path": {Type: "string", Description: "File path to read"},
			},
			Required: []string{"path"},
		},
	}
}

func (*readFile) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	path, _ := call.Args["path"].(string)
	if path == "" {
		return tool.Result{Output: "path is required", IsError: true}, nil
	}
	fs, err := sandboxFromContext(ctx)
	if err != nil {
		return tool.Result{Output: err.Error(), IsError: true}, nil
	}
	data, err := fs.Read(path)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("read error: %v", err), IsError: true}, nil
	}
	return tool.Result{Output: string(data)}, nil
}

// writeFile implements the WRITE tool.
type writeFile struct{}

func (*writeFile) Definition() tool.Definition {
	return tool.Definition{
		Name:        "WRITE",
		Description: "Write content to a file at the given path.",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"path":    {Type: "string", Description: "File path to write"},
				"content": {Type: "string", Description: "Content to write"},
			},
			Required: []string{"path", "content"},
		},
	}
}

func (*writeFile) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	path, _ := call.Args["path"].(string)
	content, _ := call.Args["content"].(string)
	if path == "" {
		return tool.Result{Output: "path is required", IsError: true}, nil
	}
	fs, err := sandboxFromContext(ctx)
	if err != nil {
		return tool.Result{Output: err.Error(), IsError: true}, nil
	}
	if err := fs.Write(path, []byte(content)); err != nil {
		return tool.Result{Output: fmt.Sprintf("write error: %v", err), IsError: true}, nil
	}
	return tool.Result{Output: "ok"}, nil
}

// patchFile implements the PATCH tool.
type patchFile struct{}

func (*patchFile) Definition() tool.Definition {
	return tool.Definition{
		Name:        "PATCH",
		Description: "Apply text replacements to a file.",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"path": {Type: "string", Description: "File path to patch"},
				"old":  {Type: "string", Description: "Text to find"},
				"new":  {Type: "string", Description: "Replacement text"},
			},
			Required: []string{"path", "old", "new"},
		},
	}
}

func (*patchFile) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	path, _ := call.Args["path"].(string)
	old, _ := call.Args["old"].(string)
	newText, _ := call.Args["new"].(string)
	if path == "" || old == "" {
		return tool.Result{Output: "path and old are required", IsError: true}, nil
	}
	fs, err := sandboxFromContext(ctx)
	if err != nil {
		return tool.Result{Output: err.Error(), IsError: true}, nil
	}
	data, err := fs.Read(path)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("read error: %v", err), IsError: true}, nil
	}
	content := string(data)
	if !strings.Contains(content, old) {
		return tool.Result{Output: "old text not found in file", IsError: true}, nil
	}
	patched := strings.Replace(content, old, newText, 1)
	if err := fs.Write(path, []byte(patched)); err != nil {
		return tool.Result{Output: fmt.Sprintf("write error: %v", err), IsError: true}, nil
	}
	return tool.Result{Output: "ok"}, nil
}

// listDir implements the LIST tool.
type listDir struct{}

func (*listDir) Definition() tool.Definition {
	return tool.Definition{
		Name:        "LIST",
		Description: "List entries in a directory.",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"path": {Type: "string", Description: "Directory path"},
			},
			Required: []string{"path"},
		},
	}
}

func (*listDir) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	path, _ := call.Args["path"].(string)
	if path == "" {
		return tool.Result{Output: "path is required", IsError: true}, nil
	}
	fs, err := sandboxFromContext(ctx)
	if err != nil {
		return tool.Result{Output: err.Error(), IsError: true}, nil
	}
	names, err := fs.List(path)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("list error: %v", err), IsError: true}, nil
	}
	return tool.Result{Output: strings.Join(names, "\n")}, nil
}

// sandboxFromContext extracts a FileSystem from the tool context.
func sandboxFromContext(ctx tool.Context) (sandbox.FileSystem, error) {
	fs := ctx.FileSystem()
	if fs == nil {
		return nil, fmt.Errorf("sandbox filesystem not available")
	}
	return fs, nil
}

// All returns all filesystem built-in tools.
func All() []tool.Tool {
	return []tool.Tool{
		&readFile{},
		&writeFile{},
		&patchFile{},
		&listDir{},
		&globFiles{},
		&searchFiles{},
	}
}

// Ensure filepath is used.
var _ = filepath.Match
