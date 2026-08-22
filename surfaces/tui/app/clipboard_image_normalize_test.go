package tuiapp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeClipboardImageDownscalesPNGAndPreservesFormat(t *testing.T) {
	src := opaqueImage(t, 3000, 1200, color.NRGBA{R: 40, G: 80, B: 120, A: 255})
	path := writeClipboardTempImage(t, ".png", encodePNG(t, src))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := normalizeClipboardImageFile(path)
	if err != nil {
		t.Fatalf("normalizeClipboardImageFile: %v", err)
	}
	cleanupPath(t, got)
	if got == path {
		t.Fatal("expected a rewritten temp file for an oversized PNG")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original clipboard PNG still exists: %v", err)
	}
	if !bytes.Equal(original[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatal("test setup did not write a PNG")
	}

	cfg, format := decodeConfig(t, got)
	if format != "png" {
		t.Fatalf("format = %q, want png", format)
	}
	if max(cfg.Width, cfg.Height) != clipboardImageMaxLongEdge {
		t.Fatalf("normalized size = %dx%d, want long edge %d", cfg.Width, cfg.Height, clipboardImageMaxLongEdge)
	}
	if cfg.Width != 2048 || cfg.Height != 819 {
		t.Fatalf("normalized size = %dx%d, want 2048x819", cfg.Width, cfg.Height)
	}
}

func TestNormalizeClipboardImageKeepsPNGAtLongEdgeLimit(t *testing.T) {
	src := opaqueImage(t, clipboardImageMaxLongEdge, 64, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	path := writeClipboardTempImage(t, ".png", encodePNG(t, src))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := normalizeClipboardImageFile(path)
	if err != nil {
		t.Fatalf("normalizeClipboardImageFile: %v", err)
	}
	if got != path {
		t.Fatalf("rewrote PNG at the long-edge limit: %q -> %q", path, got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rewrote PNG bytes at the long-edge limit")
	}
	cfg, format := decodeConfig(t, got)
	if format != "png" || cfg.Width != clipboardImageMaxLongEdge || cfg.Height != 64 {
		t.Fatalf("kept image = %s %dx%d", format, cfg.Width, cfg.Height)
	}
}

func TestNormalizeClipboardImageDownscalesOnePixelOverLongEdge(t *testing.T) {
	src := opaqueImage(t, clipboardImageMaxLongEdge+1, 32, color.NRGBA{R: 200, G: 10, B: 10, A: 255})
	path := writeClipboardTempImage(t, ".png", encodePNG(t, src))

	got, err := normalizeClipboardImageFile(path)
	if err != nil {
		t.Fatalf("normalizeClipboardImageFile: %v", err)
	}
	cleanupPath(t, got)
	cfg, format := decodeConfig(t, got)
	if format != "png" {
		t.Fatalf("format = %q, want png", format)
	}
	if max(cfg.Width, cfg.Height) != clipboardImageMaxLongEdge {
		t.Fatalf("normalized size = %dx%d, want long edge %d", cfg.Width, cfg.Height, clipboardImageMaxLongEdge)
	}
}

func TestNormalizeClipboardImagePreservesPNGAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2200, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 2200; x++ {
			if y < 4 {
				src.SetNRGBA(x, y, color.NRGBA{R: 255, A: 0})
			} else {
				src.SetNRGBA(x, y, color.NRGBA{G: 255, A: 255})
			}
		}
	}
	path := writeClipboardTempImage(t, ".png", encodePNG(t, src))

	got, err := normalizeClipboardImageFile(path)
	if err != nil {
		t.Fatalf("normalizeClipboardImageFile: %v", err)
	}
	cleanupPath(t, got)
	decoded := decodeImage(t, got)
	_, format := decodeConfig(t, got)
	if format != "png" {
		t.Fatalf("format = %q, want png", format)
	}
	if nrgbaAt(decoded, 0, 0).A != 0 {
		t.Fatalf("transparent pixel alpha = %d, want 0", nrgbaAt(decoded, 0, 0).A)
	}
	opaque := nrgbaAt(decoded, 0, decoded.Bounds().Dy()-1)
	if opaque.A != 255 {
		t.Fatalf("opaque pixel = %#v", opaque)
	}
}

func TestNormalizeClipboardImageKeepsJPEGFormat(t *testing.T) {
	src := opaqueImage(t, 2500, 1000, color.NRGBA{R: 90, G: 40, B: 20, A: 255})
	path := writeClipboardTempImage(t, ".jpg", encodeJPEG(t, src))

	got, err := normalizeClipboardImageFile(path)
	if err != nil {
		t.Fatalf("normalizeClipboardImageFile: %v", err)
	}
	cleanupPath(t, got)
	if got == path {
		t.Fatal("expected a rewritten temp file for an oversized JPEG")
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 3 || data[0] != 0xff || data[1] != 0xd8 || data[2] != 0xff {
		t.Fatalf("normalized JPEG missing SOI marker: %x", data[:min(len(data), 8)])
	}
	cfg, format := decodeConfig(t, got)
	if format != "jpeg" {
		t.Fatalf("format = %q, want jpeg", format)
	}
	if max(cfg.Width, cfg.Height) != clipboardImageMaxLongEdge {
		t.Fatalf("normalized size = %dx%d, want long edge %d", cfg.Width, cfg.Height, clipboardImageMaxLongEdge)
	}
}

func TestNormalizeClipboardImageLeavesSmallGIFUnchanged(t *testing.T) {
	src := opaqueImage(t, 64, 48, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	path := writeClipboardTempImage(t, ".gif", encodeGIF(t, src))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := normalizeClipboardImageFile(path)
	if err != nil {
		t.Fatalf("normalizeClipboardImageFile: %v", err)
	}
	if got != path {
		t.Fatalf("rewrote small GIF: %q -> %q", path, got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rewrote small GIF bytes")
	}
	_, format := decodeConfig(t, got)
	if format != "gif" {
		t.Fatalf("format = %q, want gif", format)
	}
}

func TestNormalizeClipboardImageRewritesLargeGIFAsPNG(t *testing.T) {
	src := opaqueImage(t, 2100, 80, color.NRGBA{R: 8, G: 16, B: 32, A: 255})
	path := writeClipboardTempImage(t, ".gif", encodeGIF(t, src))

	got, err := normalizeClipboardImageFile(path)
	if err != nil {
		t.Fatalf("normalizeClipboardImageFile: %v", err)
	}
	cleanupPath(t, got)
	cfg, format := decodeConfig(t, got)
	if format != "png" {
		t.Fatalf("format = %q, want png after GIF downscale", format)
	}
	if max(cfg.Width, cfg.Height) != clipboardImageMaxLongEdge {
		t.Fatalf("normalized size = %dx%d, want long edge %d", cfg.Width, cfg.Height, clipboardImageMaxLongEdge)
	}
}

func TestNormalizeClipboardImageLeavesUndecodableWebPUnchanged(t *testing.T) {
	data := []byte("RIFF\x08\x00\x00\x00WEBPVP8 ")
	path := writeClipboardTempImage(t, ".webp", data)

	got, err := normalizeClipboardImageFile(path)
	if err != nil {
		t.Fatalf("normalizeClipboardImageFile: %v", err)
	}
	if got != path {
		t.Fatalf("rewrote undecodable WebP: %q -> %q", path, got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, after) {
		t.Fatal("rewrote undecodable WebP bytes")
	}
}

func TestNormalizeClipboardImageRejectsOversizedDecodedDimensions(t *testing.T) {
	path := writeClipboardTempImage(t, ".png", pngConfigBytes(t, 100_000, 100_000))

	got, err := normalizeClipboardImageFile(path)
	if !errors.Is(err, errClipboardImageResourceLimit) {
		t.Fatalf("normalizeClipboardImageFile() error = %v, want resource limit", err)
	}
	if got != path {
		t.Fatalf("normalizeClipboardImageFile() path = %q, want original %q", got, path)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("original oversized clipboard image was removed: %v", statErr)
	}

	finalized, err := finalizeClipboardImage(path)
	if !errors.Is(err, errClipboardImageResourceLimit) {
		t.Fatalf("finalizeClipboardImage() error = %v, want resource limit", err)
	}
	if finalized != "" {
		t.Fatalf("finalizeClipboardImage() path = %q, want no pass-through path", finalized)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rejected clipboard temp file still exists: %v", statErr)
	}
}

func TestNormalizeClipboardImageDoesNotRewriteUserFiles(t *testing.T) {
	src := opaqueImage(t, 2400, 800, color.NRGBA{R: 255, G: 128, B: 0, A: 255})
	dir := t.TempDir()
	userPNG := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(userPNG, encodePNG(t, src), 0o600); err != nil {
		t.Fatal(err)
	}
	userClipboardName := filepath.Join(dir, "clipboard-shot.png")
	if err := os.WriteFile(userClipboardName, encodePNG(t, src), 0o600); err != nil {
		t.Fatal(err)
	}
	tempDir := filepath.Join(t.TempDir(), clipboardImageTempDirName)
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unprefixed := filepath.Join(tempDir, "shot.png")
	if err := os.WriteFile(unprefixed, encodePNG(t, src), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{userPNG, userClipboardName, unprefixed} {
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := normalizeClipboardImageFile(path)
		if err != nil {
			t.Fatalf("normalizeClipboardImageFile(%q): %v", path, err)
		}
		if got != path {
			t.Fatalf("rewrote user file %q -> %q", path, got)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("changed user file bytes: %q", path)
		}
		cfg, _ := decodeConfig(t, path)
		if cfg.Width != 2400 || cfg.Height != 800 {
			t.Fatalf("user file %q size changed to %dx%d", path, cfg.Width, cfg.Height)
		}
	}
}

func TestWriteClipboardImageBytesNormalizesOversizedPNG(t *testing.T) {
	src := opaqueImage(t, 2300, 100, color.NRGBA{R: 12, G: 24, B: 48, A: 255})
	names, path, err := writeClipboardImageBytes(encodePNG(t, src), "image/png")
	if err != nil {
		t.Fatalf("writeClipboardImageBytes: %v", err)
	}
	cleanupPath(t, path)
	if len(names) != 1 || names[0] != path {
		t.Fatalf("writeClipboardImageBytes names = %v path = %q", names, path)
	}
	cfg, format := decodeConfig(t, path)
	if format != "png" {
		t.Fatalf("format = %q, want png", format)
	}
	if max(cfg.Width, cfg.Height) != clipboardImageMaxLongEdge {
		t.Fatalf("normalized size = %dx%d, want long edge %d", cfg.Width, cfg.Height, clipboardImageMaxLongEdge)
	}
	if !isClipboardGeneratedImagePath(path) {
		t.Fatalf("result path is not a clipboard temp file: %q", path)
	}
}

func TestScaledClipboardImageSize(t *testing.T) {
	tests := []struct {
		width, height, wantW, wantH int
	}{
		{2048, 1024, 2048, 1024},
		{2049, 32, 2048, 32},
		{3000, 2000, 2048, 1365},
		{100, 4000, 51, 2048},
		{1, 5000, 1, 2048},
	}
	for _, tt := range tests {
		gotW, gotH := scaledClipboardImageSize(tt.width, tt.height, clipboardImageMaxLongEdge)
		if gotW != tt.wantW || gotH != tt.wantH {
			t.Fatalf("scaledClipboardImageSize(%d,%d) = %d,%d, want %d,%d", tt.width, tt.height, gotW, gotH, tt.wantW, tt.wantH)
		}
	}
}

func opaqueImage(t *testing.T, width, height int, c color.NRGBA) *image.NRGBA {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = c.A
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeGIF(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngConfigBytes(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	data := make([]byte, 13)
	binary.BigEndian.PutUint32(data[0:4], width)
	binary.BigEndian.PutUint32(data[4:8], height)
	data[8] = 8
	data[9] = 6
	chunkType := []byte("IHDR")
	if err := binary.Write(&out, binary.BigEndian, uint32(len(data))); err != nil {
		t.Fatal(err)
	}
	out.Write(chunkType)
	out.Write(data)
	if err := binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(append(chunkType, data...))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func writeClipboardTempImage(t *testing.T, ext string, data []byte) string {
	t.Helper()
	path, err := newClipboardImagePath(ext)
	if err != nil {
		t.Fatal(err)
	}
	cleanupPath(t, path)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func cleanupPath(t *testing.T, path string) {
	t.Helper()
	if path == "" {
		return
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

func decodeConfig(t *testing.T, path string) (image.Config, string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, format
}

func nrgbaAt(img image.Image, x, y int) color.NRGBA {
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

func decodeImage(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	return img
}
