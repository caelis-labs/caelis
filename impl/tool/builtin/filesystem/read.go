package filesystem

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/caelis-labs/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/caelis-labs/caelis/impl/tool/internal/argparse"
	"github.com/caelis-labs/caelis/ports/sandbox"
	"github.com/caelis-labs/caelis/ports/tool"
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
		Description: "Read a slice of one text file and return numbered lines plus cursor metadata. Use this after LIST/GLOB/SEARCH identifies a relevant file, or when exact text is needed before editing. Prefer small offsets and limits; if has_more is true, continue from next_offset. Use revision as if_revision for WRITE or PATCH stale-edit guards.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "minLength": 1, "description": "File path."},
				"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based start line."},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": t.cfg.MaxLimit, "description": "Max lines."},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Metadata: toolutil.AnnotationMetadata(true, false, true, false),
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
	if err := tool.RejectUnknownArgs(args, "path", "offset", "limit"); err != nil {
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
	info, err := fsys.Stat(targetPath)
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

	revision := contentHashRevision(hasher)
	if hasMore {
		revision = statRevision(info)
	}

	return toolutil.JSONResult(ReadToolName, map[string]any{
		"start_line":  startLine,
		"end_line":    endLine,
		"next_offset": nextOffset,
		"has_more":    hasMore,
		"revision":    revision,
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
