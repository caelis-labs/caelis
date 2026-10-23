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
	imageMaxTiles        = 8
)

type preparedImage struct {
	images []preparedImagePart
	note   string
}

// Empty data means validated pass-through. Cached derivatives never retain
// original media bytes, names, or references into canonical requests.
type preparedImagePart struct {
	data, mimeType, note string
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
	return sharedImagePreparationCache.prepareRequest(ctx, req)
}

func (cache *imagePreparationCache) prepareRequest(ctx context.Context, req *Request) error {
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
			prepared, err := cache.prepare(ctx, media.Source.Data, func() preparedImage { return prepareInlineImage(media) })
			if err != nil {
				return nil, err
			}
			if prepared.note != "" {
				out = append(out, NewTextPart(fmt.Sprintf("[Image %q: %s]", media.Name, prepared.note)))
			}
			for _, derived := range prepared.images {
				if derived.note != "" {
					out = append(out, NewTextPart(fmt.Sprintf("[Image %q: %s]", media.Name, derived.note)))
				}
				copyMedia := *media
				copyMedia.MimeType = derived.mimeType
				if derived.data != "" {
					copyMedia.Source.Data = derived.data
				}
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
	if max(cfg.Width, cfg.Height) <= imageMaxDimension && len(media.Source.Data) <= imageMaxEncodedBytes {
		return preparedImage{images: []preparedImagePart{{mimeType: mime}}}
	}
	// Re-encoding removes EXIF, so use the displayed orientation and dimensions
	// rather than rotating or mirroring camera photos as a side effect.
	cfg.Width, cfg.Height = src.Bounds().Dx(), src.Bounds().Dy()
	regions := imagePreparationRegions(src.Bounds())
	result := preparedImage{}
	for i, region := range regions {
		derived, err := encodePreparedImage(src, region)
		if err != nil {
			return omit("image could not be encoded")
		}
		mapping := region.Sub(src.Bounds().Min)
		derived.note = fmt.Sprintf("crop %d/%d of %dx%d in display orientation; source rectangle [%d,%d,%d,%d), prepared %s; map coordinates back by scaling to this rectangle and adding its top-left offset", i+1, len(regions), cfg.Width, cfg.Height, mapping.Min.X, mapping.Min.Y, mapping.Max.X, mapping.Max.Y, derived.note)
		result.images = append(result.images, derived)
	}
	return result
}

// Panoramic images are cropped along their long axis before scaling so text
// does not collapse into a narrow overview. At most eight overlapping crops
// cover the complete displayed image; extreme aspect ratios still need scaling.
func imagePreparationRegions(bounds image.Rectangle) []image.Rectangle {
	w, h := bounds.Dx(), bounds.Dy()
	long, short := max(w, h), min(w, h)
	if long <= imageMaxDimension || long < short*3 {
		return []image.Rectangle{bounds}
	}
	edge := max(short, imageMaxDimension)
	overlap := edge / 16
	// Ensure coverage including overlap without exceeding the tile budget.
	edge = max(edge, (long+(imageMaxTiles-1)*overlap+imageMaxTiles-1)/imageMaxTiles)
	count := (long - overlap + edge - overlap - 1) / (edge - overlap)
	var regions []image.Rectangle
	for i := 0; i < count; i++ {
		start := i * (long - edge) / (count - 1)
		end := start + edge
		region := bounds
		if h >= w {
			region.Min.Y, region.Max.Y = bounds.Min.Y+start, bounds.Min.Y+end
		} else {
			region.Min.X, region.Max.X = bounds.Min.X+start, bounds.Min.X+end
		}
		regions = append(regions, region)
	}
	return regions
}

func encodePreparedImage(src image.Image, region image.Rectangle) (preparedImagePart, error) {
	w, h := region.Dx(), region.Dy()
	if long := max(w, h); long > imageMaxDimension {
		w = max(1, int(int64(w)*imageMaxDimension/int64(long)))
		h = max(1, int(int64(h)*imageMaxDimension/int64(long)))
	}
	for {
		scaled := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), src, region, draw.Src, nil)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, scaled); err != nil {
			return preparedImagePart{}, err
		}
		mime := "image/png"
		if base64.StdEncoding.EncodedLen(encoded.Len()) > imageMaxEncodedBytes {
			// Flatten alpha onto white before JPEG encoding, rather than
			// turning transparent screenshots black.
			opaque := image.NewRGBA(scaled.Bounds())
			draw.Draw(opaque, opaque.Bounds(), image.White, image.Point{}, draw.Src)
			draw.Draw(opaque, opaque.Bounds(), scaled, image.Point{}, draw.Over)
			encoded.Reset()
			if err := jpeg.Encode(&encoded, opaque, &jpeg.Options{Quality: 85}); err != nil {
				return preparedImagePart{}, err
			}
			mime = "image/jpeg"
		}
		if base64.StdEncoding.EncodedLen(encoded.Len()) <= imageMaxEncodedBytes {
			return preparedImagePart{data: base64.StdEncoding.EncodeToString(encoded.Bytes()), mimeType: mime, note: fmt.Sprintf("%dx%d", w, h)}, nil
		}
		w, h = max(1, w*3/4), max(1, h*3/4)
	}
}
