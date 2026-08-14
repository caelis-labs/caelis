package filesystem

import (
	"context"
	"encoding/base64"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const transparentPixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestViewImageReturnsInlineImageForCapableModel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data, err := base64.StdEncoding.DecodeString(transparentPixelPNG)
	if err != nil {
		t.Fatalf("DecodeString(test image) error = %v", err)
	}
	path := filepath.Join(dir, "pixel.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(pixel.png) error = %v", err)
	}
	runtime, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	viewImage, err := NewViewImage(runtime)
	if err != nil {
		t.Fatalf("NewViewImage() error = %v", err)
	}

	result, err := viewImage.Call(context.Background(), tool.Call{
		Name:         ViewImageToolName,
		Input:        []byte(`{"path":"pixel.png"}`),
		RuntimeModel: viewImageTestModel{imageInput: true},
	})
	if err != nil {
		t.Fatalf("ViewImage.Call() error = %v", err)
	}
	if result.Name != ViewImageToolName || len(result.Content) != 2 {
		t.Fatalf("ViewImage result = %#v, want text and media", result)
	}
	if result.Content[0].Text == nil || result.Content[0].Text.Text == "" {
		t.Fatal("ViewImage text result is empty")
	}
	media := result.Content[1].Media
	if media == nil ||
		media.Modality != model.MediaModalityImage ||
		media.Source.Kind != model.MediaSourceInline ||
		media.MimeType != "image/png" ||
		media.Name != "pixel.png" {
		t.Fatalf("ViewImage media = %#v", media)
	}
	decoded, err := base64.StdEncoding.DecodeString(media.Source.Data)
	if err != nil {
		t.Fatalf("DecodeString(result media) error = %v", err)
	}
	if string(decoded) != string(data) {
		t.Fatal("ViewImage media bytes changed")
	}
}

func TestViewImageRequiresDeclaredImageInputCapability(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runtime, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	viewImage, err := NewViewImage(runtime)
	if err != nil {
		t.Fatalf("NewViewImage() error = %v", err)
	}
	if specs := tool.NewToolVisibility([]tool.Tool{viewImage}).ModelSpecs(); len(specs) != 1 {
		t.Fatalf("unbound ViewImage specs = %#v, want capability gate deferred without a model", specs)
	}
	modelWithoutImages := viewImageTestModel{}

	visibility := tool.NewToolVisibilityForModel([]tool.Tool{viewImage}, modelWithoutImages)
	if specs := visibility.ModelSpecs(); len(specs) != 0 {
		t.Fatalf("ViewImage specs = %#v, want hidden for text-only model", specs)
	}
	visibility.Reveal(ViewImageToolName)
	if specs := visibility.ModelSpecs(); len(specs) != 0 {
		t.Fatalf("revealed ViewImage specs = %#v, want capability gate to remain closed", specs)
	}

	_, err = viewImage.Call(context.Background(), tool.Call{
		Name:         ViewImageToolName,
		Input:        []byte(`{"path":"missing.png"}`),
		RuntimeModel: modelWithoutImages,
	})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeUnsupported {
		t.Fatalf("ViewImage.Call() error = %#v, want unsupported", err)
	}
}

func TestViewImageRejectsUnsupportedFileContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "not-image.png"), []byte("plain text"), 0o644); err != nil {
		t.Fatalf("WriteFile(not-image.png) error = %v", err)
	}
	runtime, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	viewImage, err := NewViewImage(runtime)
	if err != nil {
		t.Fatalf("NewViewImage() error = %v", err)
	}

	_, err = viewImage.Call(context.Background(), tool.Call{
		Name:         ViewImageToolName,
		Input:        []byte(`{"path":"not-image.png"}`),
		RuntimeModel: viewImageTestModel{imageInput: true},
	})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeInvalidInput {
		t.Fatalf("ViewImage.Call() error = %#v, want invalid input", err)
	}
}

func TestViewImageBoundsReadWhenFileGrowsAfterStat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	smallPath := filepath.Join(dir, "small.png")
	if err := os.WriteFile(smallPath, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(small.png) error = %v", err)
	}
	reportedInfo, err := os.Stat(smallPath)
	if err != nil {
		t.Fatalf("Stat(small.png) error = %v", err)
	}
	largePath := filepath.Join(dir, "large.png")
	if err := os.WriteFile(largePath, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(large.png) error = %v", err)
	}
	if err := os.Truncate(largePath, maxViewImageBytes+1); err != nil {
		t.Fatalf("Truncate(large.png) error = %v", err)
	}
	viewImage, err := NewViewImage(fakeRuntime{
		defaultFS: staleSizeViewImageFileSystem{
			hostFileSystem: hostFileSystem{cwd: dir},
			reportedInfo:   reportedInfo,
		},
	})
	if err != nil {
		t.Fatalf("NewViewImage() error = %v", err)
	}

	_, err = viewImage.Call(context.Background(), tool.Call{
		Name:         ViewImageToolName,
		Input:        []byte(`{"path":"large.png"}`),
		RuntimeModel: viewImageTestModel{imageInput: true},
	})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeInvalidInput {
		t.Fatalf("ViewImage.Call() error = %#v, want bounded invalid input", err)
	}
}

type staleSizeViewImageFileSystem struct {
	hostFileSystem
	reportedInfo os.FileInfo
}

func (f staleSizeViewImageFileSystem) Stat(string) (os.FileInfo, error) {
	return f.reportedInfo, nil
}

func (staleSizeViewImageFileSystem) ReadFile(string) ([]byte, error) {
	return nil, errors.New("ViewImage used unbounded ReadFile")
}

type viewImageTestModel struct {
	imageInput bool
}

func (viewImageTestModel) Name() string { return "view-image-test" }

func (viewImageTestModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(func(*model.StreamEvent, error) bool) {}
}

func (m viewImageTestModel) Capabilities() model.Capabilities {
	return model.Capabilities{ImageInput: m.imageInput}
}
