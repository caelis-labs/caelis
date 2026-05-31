package gatewaydriver

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
)

func TestContentPartsFromAttachmentsReadsImageFiles(t *testing.T) {
	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "shot.png")
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	parts, err := contentPartsFromAttachments([]Attachment{{Name: "shot.png"}}, workspace)
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

func TestContentPartsFromSubmissionInterleavesTextAndImages(t *testing.T) {
	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "shot.png")
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	parts, err := contentPartsFromSubmission("first second", []Attachment{{Name: "shot.png", Offset: len([]rune("first "))}}, workspace)
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

func TestContentPartsFromAttachmentsRejectsNonImages(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := contentPartsFromAttachments([]Attachment{{Name: "note.txt"}}, workspace); err == nil {
		t.Fatal("contentPartsFromAttachments() error = nil, want non-image rejection")
	}
}

func TestContentPartsFromAttachmentsRejectsRenamedNonImages(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "not-really.png"), []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := contentPartsFromAttachments([]Attachment{{Name: "not-really.png"}}, workspace); err == nil {
		t.Fatal("contentPartsFromAttachments() error = nil, want content-based non-image rejection")
	}
}

func TestContentPartsFromAttachmentsRejectsOversizedImages(t *testing.T) {
	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "huge.png")
	file, err := os.Create(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxAttachmentImageBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := contentPartsFromAttachments([]Attachment{{Name: "huge.png"}}, workspace); err == nil {
		t.Fatal("contentPartsFromAttachments() error = nil, want image size rejection")
	}
}
