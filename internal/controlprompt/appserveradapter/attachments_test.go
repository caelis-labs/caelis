package appserveradapter

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestContentPartsFromAttachmentsReadsImageFiles(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "shot.png")
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	parts, err := contentPartsFromAttachments([]controlprompt.Attachment{{Name: "shot.png"}}, workspace)
	if err != nil {
		t.Fatalf("contentPartsFromAttachments() error = %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1", len(parts))
	}
	part := parts[0]
	if part.Type != model.ContentPartImage {
		t.Fatalf("part.Type = %q, want image", part.Type)
	}
	if part.MimeType != "image/png" {
		t.Fatalf("part.MimeType = %q, want image/png", part.MimeType)
	}
	if part.FileName != "shot.png" {
		t.Fatalf("part.FileName = %q, want shot.png", part.FileName)
	}
	if part.Data != base64.StdEncoding.EncodeToString(raw) {
		t.Fatal("part.Data did not contain the base64 encoded image")
	}
}

func TestContentPartsFromAttachmentsReadsInlineImageData(t *testing.T) {
	t.Parallel()

	imageData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	parts, err := contentPartsFromAttachments([]controlprompt.Attachment{{
		Name:     "inline.png",
		MimeType: "image/png",
		Data:     imageData,
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("contentPartsFromAttachments(inline data) error = %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1", len(parts))
	}
	part := parts[0]
	if part.Type != model.ContentPartImage || part.MimeType != "image/png" || part.Data != imageData || part.FileName != "inline.png" {
		t.Fatalf("part = %#v, want inline png image", part)
	}
}

func TestContentPartsFromSubmissionInterleavesTextAndImages(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "shot.png")
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	parts, err := contentPartsFromSubmission("first second", []controlprompt.Attachment{{Name: "shot.png", Offset: len([]rune("first "))}}, workspace)
	if err != nil {
		t.Fatalf("contentPartsFromSubmission() error = %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("len(parts) = %d, want 3", len(parts))
	}
	if parts[0].Type != model.ContentPartText || parts[0].Text != "first " {
		t.Fatalf("parts[0] = %#v, want first text segment", parts[0])
	}
	if parts[1].Type != model.ContentPartImage || parts[1].FileName != "shot.png" {
		t.Fatalf("parts[1] = %#v, want image", parts[1])
	}
	if parts[2].Type != model.ContentPartText || parts[2].Text != "second" {
		t.Fatalf("parts[2] = %#v, want second text segment", parts[2])
	}
}

func TestDisplayInputWithAttachmentsUsesOrdinalMarkers(t *testing.T) {
	t.Parallel()

	got := displayInputWithAttachments("look here", []controlprompt.Attachment{
		{Name: "first.png", Offset: 0},
		{Name: "second.png", Offset: len([]rune("look "))},
	})
	if got != "[image #1] look [image #2] here" {
		t.Fatalf("displayInputWithAttachments() = %q, want image markers", got)
	}
}

func TestContentPartsFromAttachmentsRejectsNonImages(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := contentPartsFromAttachments([]controlprompt.Attachment{{Name: "note.txt"}}, workspace); err == nil {
		t.Fatal("contentPartsFromAttachments() error = nil, want non-image rejection")
	}
}

func TestContentPartsFromAttachmentsRejectsRenamedNonImages(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "not-really.png"), []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := contentPartsFromAttachments([]controlprompt.Attachment{{Name: "not-really.png"}}, workspace); err == nil {
		t.Fatal("contentPartsFromAttachments() error = nil, want content-based non-image rejection")
	}
}

func TestContentPartsFromAttachmentsRejectsOversizedImages(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "huge.png")
	file, err := os.Create(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(appserver.MaxPromptImageBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := contentPartsFromAttachments([]controlprompt.Attachment{{Name: "huge.png"}}, workspace); err == nil {
		t.Fatal("contentPartsFromAttachments() error = nil, want image size rejection")
	}
}

func TestAddPromptImageBytesEnforcesAggregateLimit(t *testing.T) {
	t.Parallel()

	total := appserver.MaxPromptImageTotalBytes - appserver.MaxPromptImageBytes
	if err := addPromptImageBytes(&total, appserver.MaxPromptImageBytes); err != nil {
		t.Fatalf("addPromptImageBytes at aggregate limit: %v", err)
	}
	if total != appserver.MaxPromptImageTotalBytes {
		t.Fatalf("total = %d, want %d", total, appserver.MaxPromptImageTotalBytes)
	}
	if err := addPromptImageBytes(&total, 1); err == nil {
		t.Fatal("addPromptImageBytes over aggregate limit error = nil")
	}
}

func TestContentPartsFromAttachmentsRejectsEmptyImages(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "empty.png"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := contentPartsFromAttachments([]controlprompt.Attachment{{Name: "empty.png"}}, workspace)
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("contentPartsFromAttachments() error = %v, want empty rejection", err)
	}
}

func TestContentPartsFromAttachmentsAcceptsExactPerImageLimit(t *testing.T) {
	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "exact.png")
	if err := writeSizedPNG(imagePath, appserver.MaxPromptImageBytes); err != nil {
		t.Fatal(err)
	}

	parts, err := contentPartsFromAttachments([]controlprompt.Attachment{{Name: "exact.png"}}, workspace)
	if err != nil {
		t.Fatalf("contentPartsFromAttachments() error = %v", err)
	}
	if len(parts) != 1 || parts[0].Type != model.ContentPartImage || parts[0].MimeType != "image/png" {
		t.Fatalf("parts = %#v, want one png image", parts)
	}
	if got, want := len(parts[0].Data), base64.StdEncoding.EncodedLen(appserver.MaxPromptImageBytes); got != want {
		t.Fatalf("encoded length = %d, want %d", got, want)
	}
}

func TestReadAndEncodeImageAttachmentReusesDetectedSampleAfterInPlaceRewrite(t *testing.T) {
	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "rewritten.png")
	initialSample := bytes.Repeat([]byte{'o'}, imageMIMESampleBytes)
	copy(initialSample, tinyPNG(t))
	initialRemainder := []byte("old-file-remainder")
	initial := append(append([]byte(nil), initialSample...), initialRemainder...)
	if err := os.WriteFile(imagePath, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	replacement := bytes.Repeat([]byte{'n'}, len(initial))
	copy(replacement, []byte("not an image after rewrite"))
	copy(replacement[imageMIMESampleBytes:], []byte("new-file-remainder"))
	file, err := os.Open(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	reader := &rewriteAfterFirstRead{
		file: file,
		rewrite: func() error {
			return os.WriteFile(imagePath, replacement, 0o600)
		},
	}

	mimeType, data, imageBytes, err := readAndEncodeImageAttachment(reader, "rewritten.png", info.Size())
	if reader.rewriteErr != nil {
		t.Fatalf("rewrite image attachment: %v", reader.rewriteErr)
	}
	if err != nil {
		t.Fatalf("readAndEncodeImageAttachment() error = %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatalf("decode encoded attachment: %v", err)
	}
	want := append(append([]byte(nil), initialSample...), replacement[imageMIMESampleBytes:]...)
	if mimeType != "image/png" || imageBytes != len(want) || !bytes.Equal(decoded, want) {
		t.Fatalf("encoded attachment = mime:%q bytes:%d data-prefix:%x, want detected PNG sample plus rewritten remainder",
			mimeType, imageBytes, decoded[:min(len(decoded), 16)])
	}
}

type rewriteAfterFirstRead struct {
	file       *os.File
	rewrite    func() error
	rewritten  bool
	rewriteErr error
}

func (r *rewriteAfterFirstRead) Read(p []byte) (int, error) {
	n, err := r.file.Read(p)
	if n == 0 || r.rewritten {
		return n, err
	}
	r.rewritten = true
	r.rewriteErr = r.rewrite()
	return n, err
}

func TestEncodeImageAttachmentBase64DetectsGrowthAndShrinkAfterStat(t *testing.T) {
	t.Parallel()

	png := tinyPNG(t)
	t.Run("shrink", func(t *testing.T) {
		t.Parallel()
		data, n, err := encodeImageAttachmentBase64(bytes.NewReader(png), "shot.png", int64(len(png)+50))
		if err != nil {
			t.Fatalf("shrink encode error = %v", err)
		}
		if n != len(png) {
			t.Fatalf("shrink size = %d, want %d", n, len(png))
		}
		if data != base64.StdEncoding.EncodeToString(png) {
			t.Fatal("shrink encode did not match the actual file bytes")
		}
	})
	t.Run("grow under limit", func(t *testing.T) {
		t.Parallel()
		data, n, err := encodeImageAttachmentBase64(bytes.NewReader(png), "shot.png", 8)
		if err != nil {
			t.Fatalf("growth encode error = %v", err)
		}
		if n != len(png) {
			t.Fatalf("growth size = %d, want %d", n, len(png))
		}
		if data != base64.StdEncoding.EncodeToString(png) {
			t.Fatal("growth encode did not match the actual file bytes")
		}
	})
	t.Run("shrink to empty", func(t *testing.T) {
		t.Parallel()
		_, _, err := encodeImageAttachmentBase64(bytes.NewReader(nil), "shot.png", 12)
		if err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("shrink-to-empty error = %v, want empty rejection", err)
		}
	})
	t.Run("grow past limit", func(t *testing.T) {
		t.Parallel()
		payload := make([]byte, appserver.MaxPromptImageBytes+1)
		copy(payload, png)
		_, _, err := encodeImageAttachmentBase64(bytes.NewReader(payload), "shot.png", int64(len(png)))
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("growth-past-limit error = %v, want too-large rejection", err)
		}
	})
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeSizedPNG(path string, size int) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	pngHeader := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if _, err := file.Write(pngHeader); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Truncate(int64(size)); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
