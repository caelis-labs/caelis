package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// AttachmentRef identifies one immutable incarnation of an image within a Session.
type AttachmentRef struct {
	SessionID string `json:"session_id"`
	Digest    string `json:"sha256"`
	Instance  string `json:"instance"`
	Size      int64  `json:"size"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

// ErrAttachmentUnavailable indicates that the exact image incarnation is absent
// or no longer verifiable.
var ErrAttachmentUnavailable = errors.New("image attachment unavailable")

// AttachmentReader verifies a reference before opening its bytes. A successful
// open may read a small header but does not load the body until Read.
type AttachmentReader interface {
	OpenAttachment(context.Context, string, AttachmentRef) (io.ReadCloser, error)
}

// AttachmentWriter publishes immutable bytes before their reference is used.
type AttachmentWriter interface {
	StoreAttachment(context.Context, string, []byte) (AttachmentRef, error)
}

// MapImageParts visits the images in a privately owned message or tool result.
func MapImageParts(parts []Part, visit func(*MediaPart) error) error {
	for i := range parts {
		if part := &parts[i]; part.Media != nil && part.Media.Modality == MediaModalityImage {
			if err := visit(part.Media); err != nil {
				return err
			}
		} else if part.ToolResult != nil {
			if err := MapImageParts(part.ToolResult.Content, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

type imageAttachments struct {
	session string
	reader  AttachmentReader
	writer  AttachmentWriter
	cache   *ImagePreparationCache
}

type imageAttachmentsKey struct{}

// WithImageAttachments binds native image use to the Run's Session and Host cache.
func WithImageAttachments(ctx context.Context, session string, reader AttachmentReader, writer AttachmentWriter, cache *ImagePreparationCache) context.Context {
	return context.WithValue(ctx, imageAttachmentsKey{}, imageAttachments{session: session, reader: reader, writer: writer, cache: cache})
}

// ReferenceImage publishes an original image using the current Run's Session
// authority. An unbound SDK caller retains the inline image representation.
func ReferenceImage(ctx context.Context, data []byte) (AttachmentRef, error) {
	access, _ := ctx.Value(imageAttachmentsKey{}).(imageAttachments)
	if access.writer == nil {
		return AttachmentRef{}, nil
	}
	return access.writer.StoreAttachment(ctx, access.session, data)
}

// ValidateImageAttachment checks that the bound Session can open this exact
// image incarnation. The reader verifies the reference without reading its body.
func ValidateImageAttachment(ctx context.Context, ref AttachmentRef) error {
	access, _ := ctx.Value(imageAttachmentsKey{}).(imageAttachments)
	f, err := openAttachment(ctx, access.reader, access.session, ref)
	if err != nil {
		return err
	}
	return f.Close()
}

func openAttachment(ctx context.Context, reader AttachmentReader, sessionID string, ref AttachmentRef) (io.ReadCloser, error) {
	if reader == nil || sessionID == "" || ref.SessionID != sessionID || ref.Instance == "" || ref.Digest == "" || ref.Size <= 0 {
		return nil, ErrAttachmentUnavailable
	}
	return reader.OpenAttachment(ctx, sessionID, ref)
}

// LoadAttachment opens and reads the exact Session-owned image.
func LoadAttachment(ctx context.Context, reader AttachmentReader, sessionID string, ref AttachmentRef) ([]byte, error) {
	f, err := openAttachment(ctx, reader, sessionID, ref)
	if err != nil {
		return nil, err
	}
	data, err := ReadAttachment(f, ref)
	return data, errors.Join(err, f.Close())
}

// ReadAttachment verifies size and digest before returning the original bytes.
func ReadAttachment(src io.Reader, ref AttachmentRef) ([]byte, error) {
	if ref.Size <= 0 || ref.Size > imageMaxSourceBytes {
		return nil, ErrAttachmentUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(src, ref.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != ref.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != ref.Digest {
		return nil, ErrAttachmentUnavailable
	}
	return data, nil
}
