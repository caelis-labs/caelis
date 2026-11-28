package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"io"
)

func attachmentPreparationKey(ref AttachmentRef) [sha256.Size]byte {
	spec, _ := json.Marshal(struct {
		Ref                  AttachmentRef
		Edge, Encoded, Tiles int
	}{ref, imageMaxDimension, imageMaxEncodedBytes, imageMaxTiles})
	return sha256.Sum256(spec)
}

func (c *ImagePreparationCache) prepareAttachment(ctx context.Context, media *MediaPart) (preparedImage, error) {
	access, _ := ctx.Value(imageAttachmentsKey{}).(imageAttachments)
	if access.reader == nil || access.session != media.Source.Attachment.SessionID {
		return unavailableImage(), nil
	}
	ref := media.Source.Attachment
	// A hit checks the live incarnation and reads only the header.
	original, err := access.reader.OpenAttachment(ctx, access.session, ref)
	if errors.Is(err, ErrAttachmentUnavailable) {
		return unavailableImage(), nil
	}
	if err != nil {
		return preparedImage{}, err
	}
	defer original.Close()
	result, err := c.prepareKey(ctx, attachmentPreparationKey(ref), func() (preparedImage, error) {
		if ref.Size > imageMaxSourceBytes {
			return preparedImage{note: "omitted: source exceeds the image processing byte limit"}, nil
		}
		data, err := ReadAttachment(original, ref)
		if err != nil {
			return preparedImage{}, err
		}
		return prepareAttachmentBytes(data), nil
	})
	if errors.Is(err, ErrAttachmentUnavailable) {
		return unavailableImage(), nil
	}
	return result, err
}

func prepareAttachmentBytes(data []byte) preparedImage {
	value := prepareImageBytes(data, base64.StdEncoding.EncodedLen(len(data)))
	if len(value.images) == 1 && value.images[0].data == "" {
		value.images[0].data = base64.StdEncoding.EncodeToString(data)
	}
	return value
}

func unavailableImage() preparedImage {
	return preparedImage{note: "attachment unavailable"}
}

const imageWarmSourceBytes = 256 << 10
const imageWarmEdge = 512

// PrewarmImages offers the first small PNG or JPEG already selected by the
// model message. Large or busy preparations are left to foreground demand.
func PrewarmImages(ctx context.Context, message *Message) {
	access, _ := ctx.Value(imageAttachmentsKey{}).(imageAttachments)
	if access.cache == nil || access.reader == nil || message == nil || ctx.Err() != nil {
		return
	}
	var selected AttachmentRef
	_ = MapImageParts(message.Parts, func(media *MediaPart) error {
		ref := media.Source.Attachment
		if selected.Instance == "" && media.Source.Kind == MediaSourceAttachment && access.session == ref.SessionID &&
			(media.MimeType == "image/png" || media.MimeType == "image/jpeg") &&
			ref.Size > 0 && ref.Size <= imageWarmSourceBytes && warmImageDimensions(ref.Width, ref.Height) &&
			base64.StdEncoding.EncodedLen(int(ref.Size))+384 <= access.cache.maxBytes && access.cache.limit > 0 && access.cache.ttl > 0 {
			selected = ref
		}
		return nil
	})
	if selected.Instance == "" {
		return
	}
	access.cache.prewarm(ctx, attachmentPreparationKey(selected), func(ctx context.Context) (preparedImage, error) {
		f, err := access.reader.OpenAttachment(ctx, access.session, selected)
		if err != nil {
			return preparedImage{}, err
		}
		defer f.Close()
		data, err := ReadAttachment(warmAttachmentReader{ctx, f}, selected)
		if err != nil {
			return preparedImage{}, err
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || !warmImageDimensions(cfg.Width, cfg.Height) || (format != "png" && format != "jpeg") || ctx.Err() != nil {
			return preparedImage{}, ErrAttachmentUnavailable
		}
		return prepareAttachmentBytes(data), nil
	})
}

func warmImageDimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= imageWarmEdge && height <= imageWarmEdge
}

type warmAttachmentReader struct {
	ctx context.Context
	src io.Reader
}

func (r warmAttachmentReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}
