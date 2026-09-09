package filesystem

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const ReadToolName = "Read"

const (
	maxReadLineBytes     = 8 * 1024 * 1024
	maxReadReturnedBytes = 8 * 1024 * 1024
)

var errReadLineTooLong = errors.New("line exceeds the 8MiB Read limit")

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

func normalizedReadConfig(cfg ReadConfig) ReadConfig {
	if cfg.DefaultLimit <= 0 || cfg.MaxLimit <= 0 {
		cfg = DefaultReadConfig()
	}
	if cfg.DefaultLimit > cfg.MaxLimit {
		cfg.DefaultLimit = cfg.MaxLimit
	}
	return cfg
}

type ReadTool struct {
	cfg     ReadConfig
	runtime sandbox.Runtime
}

func NewRead(cfg ReadConfig, runtime sandbox.Runtime) (*ReadTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &ReadTool{cfg: normalizedReadConfig(cfg), runtime: resolvedRuntime}, nil
}

func (t *ReadTool) Definition() tool.Definition {
	cfg := normalizedReadConfig(t.cfg)
	return tool.Definition{
		Name:        ReadToolName,
		Description: "Read a slice of one text file and return numbered lines plus cursor metadata. Use this after Glob or Grep identifies a relevant file, or when exact text is needed before editing. Prefer small offsets and limits; if has_more is true, continue from next_offset. Use revision as if_revision for Write stale-edit guards. Regular files only; a line over 8MiB is an error, not a truncated line. Content pages at whole-line boundaries up to 8MiB.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "minLength": 1, "description": "File path."},
				"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based start line."},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": cfg.MaxLimit, "description": "Max lines."},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Metadata:              toolutil.AnnotationMetadata(true, false, true, false),
		ExecutionRequirements: fileSystemExecutionRequirements(),
	}
}

func (t *ReadTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	cfg := normalizedReadConfig(t.cfg)
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
		return tool.Result{}, fmt.Errorf("arg %q must be >= 0", "offset")
	}
	limit, err := argparse.Int(args, "limit", cfg.DefaultLimit)
	if err != nil {
		return tool.Result{}, err
	}
	if limit <= 0 {
		limit = cfg.DefaultLimit
	}
	if limit > cfg.MaxLimit {
		limit = cfg.MaxLimit
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
	file, err := openRegularFile(fsys, targetPath)
	if err != nil {
		return tool.Result{}, err
	}
	defer file.Close()

	hasher := contentHasher()
	reader := bufio.NewReader(file)

	var (
		lineNo        int
		lines         []string
		returnedBytes int
		hasMore       bool
	)
	for {
		rawLine, readErr := readBoundedLine(ctx, reader, maxReadLineBytes)
		if errors.Is(readErr, errReadLineTooLong) {
			return tool.Result{}, readLineTooLongError(targetPath)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return tool.Result{}, readErr
		}
		if len(rawLine) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		_, _ = hasher.Write(rawLine)
		lineNo++
		if lineNo <= offset {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		line := trimReadLineEnding(string(rawLine))
		formattedLen := formattedReadLineBytes(lineNo, line, len(lines) > 0)
		if formattedLen > maxReadReturnedBytes {
			return tool.Result{}, readLineTooLongError(targetPath)
		}
		if returnedBytes+formattedLen > maxReadReturnedBytes {
			hasMore = true
			break
		}
		lines = append(lines, line)
		returnedBytes += formattedLen
		if len(lines) >= limit {
			if !errors.Is(readErr, io.EOF) {
				if err := ctx.Err(); err != nil {
					return tool.Result{}, err
				}
				if _, peekErr := reader.Peek(1); peekErr == nil {
					hasMore = true
				} else if peekErr != io.EOF {
					return tool.Result{}, peekErr
				}
			}
			break
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	var content strings.Builder
	content.Grow(returnedBytes)
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

func readBoundedLine(ctx context.Context, reader *bufio.Reader, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errReadLineTooLong
	}
	var buf []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, readErr := reader.ReadSlice('\n')
		if len(buf)+len(chunk) > maxBytes {
			return nil, errReadLineTooLong
		}
		if len(chunk) > 0 {
			buf = append(buf, chunk...)
		}
		switch {
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return buf, io.EOF
		case readErr != nil:
			return nil, readErr
		default:
			return buf, nil
		}
	}
}

func formattedReadLineBytes(lineNo int, line string, leadingNewline bool) int {
	n := len(strconv.Itoa(lineNo)) + 2 + len(line)
	if leadingNewline {
		n++
	}
	return n
}

func readLineTooLongError(path string) error {
	return tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("Read path %q has a line that exceeds the 8MiB limit", path))
}

func trimReadLineEnding(line string) string {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line
}

var _ tool.Tool = (*ReadTool)(nil)
