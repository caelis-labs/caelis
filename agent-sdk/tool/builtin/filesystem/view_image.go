package filesystem

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const (
	ViewImageToolName = "ViewImage"
	maxViewImageBytes = int64(20_000_000)
)

// ViewImageTool reads one local image.
type ViewImageTool struct {
	runtime sandbox.Runtime
}

func NewViewImage(runtime sandbox.Runtime) (*ViewImageTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &ViewImageTool{runtime: resolvedRuntime}, nil
}

func (t *ViewImageTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ViewImageToolName,
		Description: "View one local PNG, JPEG, GIF, or WebP image. Use this only when visual inspection is needed; use Read for text files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "minLength": 1, "description": "Image file path."},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
	}
}

func (t *ViewImageTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	return t.CallWithImageInput(ctx, call, true)
}

// CallWithImageInput checks the receiving model's image capability before any file access.
func (t *ViewImageTool) CallWithImageInput(ctx context.Context, call tool.Call, imageInput bool) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	if err := tool.RejectUnknownArgs(args, "path"); err != nil {
		return tool.Result{}, err
	}
	pathArg, err := argparse.String(args, "path", true)
	if err != nil {
		return tool.Result{}, err
	}
	if !imageInput {
		return tool.Result{}, tool.NewError(tool.ErrorCodeUnsupported, "ViewImage requires a model that supports image input")
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
	if info.Size() > maxViewImageBytes {
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("ViewImage file %q exceeds the %d byte limit", targetPath, maxViewImageBytes))
	}
	file, err := openRegularFile(fsys, targetPath)
	if err != nil {
		return tool.Result{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxViewImageBytes+1))
	if err != nil {
		return tool.Result{}, err
	}
	if int64(len(data)) > maxViewImageBytes {
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("ViewImage file %q exceeds the %d byte limit", targetPath, maxViewImageBytes))
	}
	mimeType, ok := supportedImageMIMEType(data)
	if !ok {
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("ViewImage supports only PNG, JPEG, GIF, and WebP files: %q", targetPath))
	}

	return tool.Result{
		Content: []tool.ContentBlock{
			tool.Text(fmt.Sprintf("Viewed image %q (%s, %d bytes).", targetPath, mimeType, len(data))),
			tool.Image{Data: data, MIMEType: mimeType, Name: filepath.Base(targetPath)},
		},
	}, nil
}

func supportedImageMIMEType(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", true
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg", true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", true
	default:
		return "", false
	}
}

var _ tool.Tool = (*ViewImageTool)(nil)
