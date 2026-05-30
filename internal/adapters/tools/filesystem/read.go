package filesystem

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

const (
	defaultReadLimit = 200
	maxReadLimit     = 400
)

type ReadFileTool struct {
	Sandbox sandbox.Runtime
}

type readFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func NewReadFileTool(runtime sandbox.Runtime) (*ReadFileTool, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tools/filesystem: sandbox runtime is required")
	}
	return &ReadFileTool{Sandbox: runtime}, nil
}

func (t *ReadFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ReadFileToolName,
		Description: "Read a numbered slice of a text file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "File path."},
				"offset": map[string]any{"type": "integer", "description": "Zero-based start line."},
				"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines."},
			},
			"required":             []any{"path"},
			"additionalProperties": false,
		},
	}
}

func (t *ReadFileTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := checkContext(ctx); err != nil {
		return tool.Result{}, err
	}
	var input readFileInput
	if err := decodeInput(call, &input); err != nil {
		return tool.Result{}, err
	}
	if input.Offset < 0 {
		return tool.Result{}, fmt.Errorf("tools/filesystem: offset must be >= 0")
	}
	limit := clampLimit(input.Limit, defaultReadLimit, maxReadLimit)
	fsys, err := runtimeFileSystem(t.Sandbox, call.Meta)
	if err != nil {
		return tool.Result{}, err
	}
	target, err := normalizePath(fsys, input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := fsys.Stat(target)
	if err != nil {
		return tool.Result{}, err
	}
	file, err := fsys.Open(target)
	if err != nil {
		return tool.Result{}, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	lineNo := 0
	lines := make([]string, 0, limit)
	hasMore := false
	for {
		rawLine, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return tool.Result{}, readErr
		}
		if rawLine == "" && readErr == io.EOF {
			break
		}
		lineNo++
		if lineNo <= input.Offset {
			if readErr == io.EOF {
				break
			}
			continue
		}
		lines = append(lines, trimLineEnding(rawLine))
		if len(lines) >= limit {
			if readErr != io.EOF {
				if _, peekErr := reader.Peek(1); peekErr == nil {
					hasMore = true
				} else if peekErr != io.EOF {
					return tool.Result{}, peekErr
				}
			}
			break
		}
		if readErr == io.EOF {
			break
		}
	}

	var content strings.Builder
	for i, line := range lines {
		if i > 0 {
			content.WriteByte('\n')
		}
		fmt.Fprintf(&content, "%d: %s", input.Offset+i+1, line)
	}
	startLine := 0
	endLine := 0
	if len(lines) > 0 {
		startLine = input.Offset + 1
		endLine = input.Offset + len(lines)
	}
	nextOffset := endLine
	if len(lines) == 0 {
		nextOffset = lineNo
	}
	return jsonResult(call, ReadFileToolName, map[string]any{
		"path":        target,
		"start_line":  startLine,
		"end_line":    endLine,
		"next_offset": nextOffset,
		"has_more":    hasMore,
		"revision":    fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano()),
		"content":     content.String(),
	}, map[string]any{
		"path":      target,
		"exhausted": len(lines) == 0 && input.Offset >= lineNo,
	})
}

func trimLineEnding(line string) string {
	line = strings.TrimSuffix(line, "\n")
	return strings.TrimSuffix(line, "\r")
}

var _ tool.Tool = (*ReadFileTool)(nil)
