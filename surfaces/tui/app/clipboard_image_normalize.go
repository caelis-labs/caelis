package tuiapp

// Clipboard image normalization rewrites only clipboard-generated temp files.
// Oversized images are downscaled when the long edge exceeds 2048. PNG, GIF,
// and alpha sources stay PNG; JPEG sources stay JPEG. User files are not
// rewritten, and undecodable clipboard bytes including WebP are left intact.

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

const (
	clipboardImageMaxLongEdge  = 2048
	clipboardImageMaxPixels    = 32_000_000
	clipboardImageJPEGQuality  = 90
	clipboardImageTempDirName  = "caelis-clipboard"
	clipboardImageTempNamePref = "clipboard-"
)

var errClipboardImageResourceLimit = errors.New("clipboard image exceeds the decode resource limit")

func finalizeClipboardImage(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	normalized, err := normalizeClipboardImageFile(path)
	if err == nil {
		return normalized, nil
	}
	if errors.Is(err, errClipboardImageResourceLimit) {
		_ = os.Remove(path)
		return "", err
	}
	if _, statErr := os.Stat(path); statErr == nil {
		return path, nil
	}
	return "", err
}

func finalizeClipboardImageResult(path string) ([]string, string, error) {
	path, err := finalizeClipboardImage(path)
	if err != nil {
		return nil, "", err
	}
	if path == "" {
		return nil, "", nil
	}
	return []string{path}, path, nil
}

func normalizeClipboardImageFile(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return path, nil
	}
	if !isClipboardGeneratedImagePath(path) {
		return path, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return path, err
	}
	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		_ = file.Close()
		return path, nil //nolint:nilerr // Unsupported clipboard formats pass through unchanged.
	}
	if clipboardImageExceedsDecodeBudget(cfg.Width, cfg.Height) {
		_ = file.Close()
		return path, fmt.Errorf("%w: %dx%d exceeds %d pixels", errClipboardImageResourceLimit, cfg.Width, cfg.Height, clipboardImageMaxPixels)
	}
	if _, seekErr := file.Seek(0, 0); seekErr != nil {
		_ = file.Close()
		return path, seekErr
	}
	if cfg.Width < 1 || cfg.Height < 1 || max(cfg.Width, cfg.Height) <= clipboardImageMaxLongEdge {
		_ = file.Close()
		return path, nil
	}
	img, decodedFormat, err := image.Decode(file)
	_ = file.Close()
	if err != nil {
		return path, err
	}
	if decodedFormat != "" {
		format = decodedFormat
	}
	width, height := scaledClipboardImageSize(cfg.Width, cfg.Height, clipboardImageMaxLongEdge)
	scaled := downscaleImage(img, width, height)
	outFormat := clipboardImageOutputFormat(format)
	outPath, err := newClipboardImagePath(clipboardImageExtension(outFormat))
	if err != nil {
		return path, err
	}
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return path, err
	}
	encodeErr := encodeClipboardImage(out, scaled, outFormat)
	closeErr := out.Close()
	if encodeErr != nil || closeErr != nil {
		_ = os.Remove(outPath)
		if encodeErr != nil {
			return path, encodeErr
		}
		return path, closeErr
	}
	_ = os.Remove(path)
	return outPath, nil
}

func clipboardImageExceedsDecodeBudget(width, height int) bool {
	return width > 0 && height > 0 && width > clipboardImageMaxPixels/height
}

func isClipboardGeneratedImagePath(path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return false
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, clipboardImageTempNamePref) {
		return false
	}
	return filepath.Base(filepath.Dir(path)) == clipboardImageTempDirName
}

func scaledClipboardImageSize(width, height, maxLongEdge int) (int, int) {
	if width < 1 || height < 1 || maxLongEdge < 1 {
		return width, height
	}
	long := max(width, height)
	if long <= maxLongEdge {
		return width, height
	}
	scaledWidth := (width*maxLongEdge + long/2) / long
	scaledHeight := (height*maxLongEdge + long/2) / long
	if scaledWidth < 1 {
		scaledWidth = 1
	}
	if scaledHeight < 1 {
		scaledHeight = 1
	}
	return scaledWidth, scaledHeight
}

func clipboardImageOutputFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg", "jpg":
		return "jpeg"
	default:
		return "png"
	}
}

func clipboardImageExtension(format string) string {
	if format == "jpeg" {
		return ".jpg"
	}
	return ".png"
}

func encodeClipboardImage(file *os.File, img image.Image, format string) error {
	if format == "jpeg" {
		return jpeg.Encode(file, img, &jpeg.Options{Quality: clipboardImageJPEGQuality})
	}
	return png.Encode(file, img)
}

func downscaleImage(src image.Image, width, height int) image.Image {
	if src == nil {
		return nil
	}
	bounds := src.Bounds()
	if bounds.Dx() == width && bounds.Dy() == height {
		return src
	}
	nrgba := imageToNRGBA(src)
	if nrgba.Bounds().Dx() == width && nrgba.Bounds().Dy() == height {
		return nrgba
	}
	return downscaleNRGBA(nrgba, width, height)
}

func imageToNRGBA(src image.Image) *image.NRGBA {
	if nrgba, ok := src.(*image.NRGBA); ok && nrgba.Rect.Min == image.Pt(0, 0) {
		clone := image.NewNRGBA(nrgba.Bounds())
		copy(clone.Pix, nrgba.Pix)
		return clone
	}
	bounds := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Src)
	return dst
}

func downscaleNRGBA(src *image.NRGBA, width, height int) *image.NRGBA {
	srcWidth := src.Bounds().Dx()
	srcHeight := src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	minX := src.Bounds().Min.X
	minY := src.Bounds().Min.Y
	for y := 0; y < height; y++ {
		srcY0 := y * srcHeight / height
		srcY1 := (y + 1) * srcHeight / height
		if srcY1 <= srcY0 {
			srcY1 = srcY0 + 1
		}
		for x := 0; x < width; x++ {
			srcX0 := x * srcWidth / width
			srcX1 := (x + 1) * srcWidth / width
			if srcX1 <= srcX0 {
				srcX1 = srcX0 + 1
			}
			var rSum, gSum, bSum, aSum, count uint64
			for srcY := srcY0; srcY < srcY1; srcY++ {
				offset := src.PixOffset(minX+srcX0, minY+srcY)
				for srcX := srcX0; srcX < srcX1; srcX++ {
					rSum += uint64(src.Pix[offset+0])
					gSum += uint64(src.Pix[offset+1])
					bSum += uint64(src.Pix[offset+2])
					aSum += uint64(src.Pix[offset+3])
					offset += 4
					count++
				}
			}
			dstOffset := dst.PixOffset(x, y)
			dst.Pix[dstOffset+0] = uint8(rSum / count)
			dst.Pix[dstOffset+1] = uint8(gSum / count)
			dst.Pix[dstOffset+2] = uint8(bSum / count)
			dst.Pix[dstOffset+3] = uint8(aSum / count)
		}
	}
	return dst
}
