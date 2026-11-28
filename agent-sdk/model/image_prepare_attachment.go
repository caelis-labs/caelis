package model

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
)

func (c *ImagePreparationCache) prepareAttachment(ctx context.Context, media *MediaPart, warm bool) (preparedImage, error) {
	access, _ := ctx.Value(imageAttachmentsKey{}).(imageAttachments)
	if access.reader == nil || access.session != media.Source.Attachment.SessionID {
		return unavailableImage(), nil
	}
	ref := media.Source.Attachment
	// Check incarnation on every use, including cache hits. Opening the file
	// reads the header only and pins that incarnation for an in-flight read.
	original, err := access.reader.OpenAttachment(ctx, access.session, ref)
	if errors.Is(err, ErrAttachmentUnavailable) {
		return unavailableImage(), nil
	}
	if err != nil {
		return preparedImage{}, err
	}
	defer original.Close()
	// The preparation specification is fixed for this cache. Include both the
	// image incarnation and specification when forming the key.
	spec, _ := json.Marshal(struct {
		Ref                  AttachmentRef
		Edge, Encoded, Tiles int
	}{ref, imageMaxDimension, imageMaxEncodedBytes, imageMaxTiles})
	result, err := c.prepareKey(ctx, sha256.Sum256(spec), func() (preparedImage, error) {
		if ref.Size > imageMaxSourceBytes {
			return preparedImage{note: "omitted: source exceeds the image processing byte limit"}, nil
		}
		data, err := ReadAttachment(original, ref)
		if err != nil {
			return preparedImage{}, err
		}
		value := prepareImageBytes(data, base64.StdEncoding.EncodedLen(len(data)))
		if len(value.images) == 1 && value.images[0].data == "" {
			value.images[0].data = base64.StdEncoding.EncodeToString(data)
		}
		return value, nil
	}, warm)
	if errors.Is(err, ErrAttachmentUnavailable) {
		return unavailableImage(), nil
	}
	return result, err
}

func unavailableImage() preparedImage {
	return preparedImage{note: "attachment unavailable"}
}

// PrewarmImages offers one bounded, best-effort preparation job. Active
// requests never wait for this job and unrelated callers cannot queue behind it.
func PrewarmImages(ctx context.Context, message *Message) {
	access, _ := ctx.Value(imageAttachmentsKey{}).(imageAttachments)
	if access.cache == nil || message == nil || ctx.Err() != nil {
		return
	}
	var images []MediaPart
	_ = MapImageParts(message.Parts, func(media *MediaPart) error {
		if media.Source.Kind == MediaSourceAttachment && len(images) < 4 {
			images = append(images, *media)
		}
		return nil
	})
	if len(images) == 0 {
		return
	}
	select {
	case access.cache.warming <- struct{}{}:
		go func() {
			defer func() { <-access.cache.warming }()
			for i := range images {
				if ctx.Err() != nil {
					return
				}
				_, _ = access.cache.prepareAttachment(ctx, &images[i], true)
			}
		}()
	default:
	}
}
