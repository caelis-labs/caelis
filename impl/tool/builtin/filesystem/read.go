package filesystem

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const ReadToolName = "READ"

type ReadConfig struct {
	DefaultLimit int
	MaxLimit     int
}

func DefaultReadConfig() ReadConfig {
	return ReadConfig{
		DefaultLimit: 200,
		MaxLimit:     400,
	}
}

type ReadTool struct {
	cfg     ReadConfig
	runtime sandbox.Runtime
}

func NewRead(cfg ReadConfig, runtime sandbox.Runtime) (*ReadTool, error) {
	if cfg.DefaultLimit <= 0 || cfg.MaxLimit <= 0 {
		cfg = DefaultReadConfig()
	}
	if cfg.DefaultLimit > cfg.MaxLimit {
		cfg.DefaultLimit = cfg.MaxLimit
	}
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &ReadTool{cfg: cfg, runtime: resolvedRuntime}, nil
}

func (t *ReadTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ReadToolName,
		Description: "Read part of a text file by line range.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "File path, absolute or relative."},
				"offset": map[string]any{"type": "integer", "description": "Zero-based starting line offset."},
				"limit":  map[string]any{"type": "integer", "description": "Optional max lines to read."},
			},
			"required": []string{"path"},
		},
	}
}

func (t *ReadTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	pathArg, err := argparse.String(args, "path", true)
	if err != nil {
		return tool.Result{}, err
	}
	offset, err := argparse.Int(args, "offset", 0)
	if err != nil {
		return tool.Result{}, err
	}
	if offset < 0 {
		return tool.Result{}, fmt.Errorf("tool: arg %q must be >= 0", "offset")
	}
	limit, err := argparse.Int(args, "limit", t.cfg.DefaultLimit)
	if err != nil {
		return tool.Result{}, err
	}
	if limit <= 0 {
		limit = t.cfg.DefaultLimit
	}
	if limit > t.cfg.MaxLimit {
		limit = t.cfg.MaxLimit
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	targetPath, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return tool.Result{}, err
	}
	file, err := fsys.Open(targetPath)
	if err != nil {
		return tool.Result{}, err
	}
	defer file.Close()

	hasher := contentHasher()
	reader := bufio.NewReader(file)

	var (
		lineNo  int
		lines   []string
		hasMore bool
	)
	for {
		rawLine, readErr := reader.ReadString('\n')
		if rawLine != "" {
			_, _ = hasher.Write([]byte(rawLine))
		}
		if readErr != nil && readErr != io.EOF {
			return tool.Result{}, readErr
		}
		if rawLine == "" && readErr == io.EOF {
			break
		}
		lineNo++
		if lineNo <= offset {
			if readErr == io.EOF {
				break
			}
			continue
		}
		lines = append(lines, trimReadLineEnding(rawLine))
		if len(lines) >= limit {
			copied, copyErr := io.Copy(hasher, reader)
			if copyErr != nil {
				return tool.Result{}, copyErr
			}
			hasMore = copied > 0
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
		fmt.Fprintf(&content, "%d: %s", offset+i+1, line)
	}

	startLine := 0
	endLine := 0
	if len(lines) > 0 {
		startLine = offset + 1
		endLine = offset + len(lines)
	}
	nextOffset := endLine
	if len(lines) == 0 {
		nextOffset = lineNo
	}
	exhausted := len(lines) == 0 && offset >= lineNo

	return toolutil.JSONResult(ReadToolName, map[string]any{
		"start_line":  startLine,
		"end_line":    endLine,
		"next_offset": nextOffset,
		"has_more":    hasMore,
		"revision":    contentHashRevision(hasher),
		"content":     content.String(),
	}, map[string]any{
		"path":      targetPath,
		"exhausted": exhausted,
	})
}

func trimReadLineEnding(line string) string {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line
}

var _ tool.Tool = (*ReadTool)(nil)
