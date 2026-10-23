package model

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"iter"

	"github.com/disintegration/imaging"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// Shared conservative inline limits also fit providers whose image limits
	// shrink as conversation history accumulates. The byte budget is base64,
	// not decoded file size, and leaves headroom below a 5 MB image limit.
	imageMaxDimension    = 2048
	imageMaxEncodedBytes = 4_500_000
	imageMaxSourceBytes  = 32 * 1024 * 1024
	imageMaxSourcePixels = 64 * 1024 * 1024
)

type preparedImage struct {
	media *MediaPart
	note  string
}

// generatePrepared is the final shared boundary for actual provider attempts.
// Preparation changes only a private request snapshot, including old tool
// results, so resuming a session repairs its wire input without rewriting
// canonical history. Notices stay beside their image inside the same content
// container; inserting messages here could break parallel tool-call pairing.
func generatePrepared(ctx context.Context, llm LLM, req *Request) iter.Seq2[*StreamEvent, error] {
	return func(yield func(*StreamEvent, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		if err := prepareRequestImages(ctx, req); err != nil {
			yield(nil, err)
			return
		}
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		for event, err := range llm.Generate(ctx, req) {
			if !yield(event, err) {
				return
			}
		}
	}
}

func prepareRequestImages(ctx context.Context, req *Request) error {
	if req == nil {
		return nil
	}
	// One request can repeat the same image across message/tool evidence.
	// Memoize only for this attempt, never retain private media globally.
	cache := make(map[MediaSource]preparedImage)
	var prepare func([]Part) ([]Part, error)
	prepare = func(parts []Part) ([]Part, error) {
		if len(parts) == 0 {
			return parts, nil
		}
		out := make([]Part, 0, len(parts))
		for _, part := range parts {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if part.ToolResult != nil {
				content, err := prepare(part.ToolResult.Content)
				if err != nil {
					return nil, err
				}
				part.ToolResult.Content = content
			}
			media := part.Media
			if part.Kind != PartKindMedia || media == nil || media.Modality != MediaModalityImage || media.Source.Kind != MediaSourceInline {
				out = append(out, part)
				continue
			}
			prepared, ok := cache[media.Source]
			if !ok {
				prepared = prepareInlineImage(media)
				cache[media.Source] = prepared
			}
			if prepared.note != "" {
				out = append(out, NewTextPart(fmt.Sprintf("[Image %q: %s]", media.Name, prepared.note)))
			}
			if prepared.media != nil {
				copyMedia := *prepared.media
				copyMedia.Name = media.Name
				part.Media = &copyMedia
				out = append(out, part)
			}
		}
		return out, nil
	}
	var err error
	if req.Instructions, err = prepare(req.Instructions); err != nil {
		return err
	}
	for i := range req.Messages {
		if req.Messages[i].Parts, err = prepare(req.Messages[i].Parts); err != nil {
			return err
		}
	}
	return nil
}

func prepareInlineImage(media *MediaPart) preparedImage {
	omit := func(reason string) preparedImage {
		return preparedImage{note: "omitted: " + reason + "; provide a smaller valid PNG, JPEG, GIF, or WebP image"}
	}
	if len(media.Source.Data) > base64.StdEncoding.EncodedLen(imageMaxSourceBytes) {
		return omit("source exceeds the image processing byte limit")
	}
	data, err := base64.StdEncoding.DecodeString(media.Source.Data)
	if err != nil || len(data) > imageMaxSourceBytes {
		return omit("invalid or oversized base64 image")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return omit("unsupported or invalid image data")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > imageMaxSourcePixels/cfg.Height {
		return omit("source exceeds the image processing pixel limit")
	}
	mime := "image/" + format
	switch format {
	case "png", "jpeg", "gif", "webp":
	default:
		return omit("unsupported image format")
	}
	// Decode even small images: a valid header alone does not establish that
	// the body is usable, and replaying corrupt bytes would wedge every turn.
	src, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return omit("image could not be decoded")
	}
	copyMedia := *media
	copyMedia.MimeType = mime
	if max(cfg.Width, cfg.Height) <= imageMaxDimension && len(media.Source.Data) <= imageMaxEncodedBytes {
		return preparedImage{media: &copyMedia}
	}
	// Re-encoding removes EXIF, so use the displayed orientation and dimensions
	// rather than rotating or mirroring camera photos as a side effect.
	cfg.Width, cfg.Height = src.Bounds().Dx(), src.Bounds().Dy()
	long := max(cfg.Width, cfg.Height)
	w, h := cfg.Width, cfg.Height
	if long > imageMaxDimension {
		w = max(1, int(int64(cfg.Width)*imageMaxDimension/int64(long)))
		h = max(1, int(int64(cfg.Height)*imageMaxDimension/int64(long)))
	}
	for {
		scaled := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), src, src.Bounds(), draw.Src, nil)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, scaled); err != nil {
			return omit("image could not be encoded")
		}
		mime = "image/png"
		if base64.StdEncoding.EncodedLen(encoded.Len()) > imageMaxEncodedBytes {
			// Flatten alpha onto white before JPEG encoding, rather than
			// turning transparent screenshots black.
			opaque := image.NewRGBA(scaled.Bounds())
			draw.Draw(opaque, opaque.Bounds(), image.White, image.Point{}, draw.Src)
			draw.Draw(opaque, opaque.Bounds(), scaled, image.Point{}, draw.Over)
			encoded.Reset()
			if err := jpeg.Encode(&encoded, opaque, &jpeg.Options{Quality: 85}); err != nil {
				return omit("image could not be encoded")
			}
			mime = "image/jpeg"
		}
		if base64.StdEncoding.EncodedLen(encoded.Len()) <= imageMaxEncodedBytes {
			copyMedia.Source.Data = base64.StdEncoding.EncodeToString(encoded.Bytes())
			copyMedia.MimeType = mime
			return preparedImage{media: &copyMedia, note: fmt.Sprintf("prepared from %dx%d to %dx%d for model input; coordinates refer to the prepared image", cfg.Width, cfg.Height, w, h)}
		}
		w, h = max(1, w*3/4), max(1, h*3/4)
	}
}
