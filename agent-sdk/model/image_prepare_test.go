package model

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

func imageTestPart(t *testing.T, width, height int) Part {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.SetNRGBA(0, 0, color.NRGBA{R: 120, G: 60, B: 30, A: 128})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return NewMediaPart(MediaModalityImage, MediaSource{Kind: MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data.Bytes())}, "image/png", "shot.png")
}

func TestGeneratePreparesImagesAcrossHistoryAndRetries(t *testing.T) {
	t.Parallel()
	imagePart := imageTestPart(t, 2646, 12249)
	req := &Request{Messages: []Message{
		{Role: RoleUser, Parts: []Part{NewTextPart("inspect"), imagePart}},
		MessageFromToolCalls(RoleAssistant, []ToolCall{{ID: "view", Name: "ViewImage", Args: `{}`}, {ID: "other", Name: "Read", Args: `{}`}}, ""),
		{Role: RoleTool, Parts: []Part{
			{Kind: PartKindToolResult, ToolResult: &ToolResultPart{ToolUseID: "view", Name: "ViewImage", Content: []Part{NewTextPart("evidence"), imagePart}}},
			{Kind: PartKindToolResult, ToolResult: &ToolResultPart{ToolUseID: "other", Name: "Read", Content: []Part{NewTextPart("other result")}}},
		}},
		NewTextMessage(RoleUser, "continue"),
	}}
	original := CloneRequest(req)
	probe := &retryTestLLM{errs: []error{errors.New("temporary outage"), nil}}
	wrapped := WithRetry(probe, RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
	for _, err := range wrapped.Generate(t.Context(), req) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(req, original) {
		t.Fatal("preparation changed canonical input")
	}
	if len(probe.seenReqs) != 2 || !reflect.DeepEqual(probe.seenReqs[0], probe.seenReqs[1]) {
		t.Fatal("retry did not receive identical prepared input")
	}
	prepared := probe.seenReqs[0]
	if len(prepared.Messages) != 4 || prepared.Messages[2].Parts[0].ToolResult.ToolUseID != "view" || prepared.Messages[2].Parts[1].ToolResult.ToolUseID != "other" {
		t.Fatal("preparation changed message or tool-call pairing")
	}
	userParts := prepared.Messages[0].Parts
	toolParts := prepared.Messages[2].Parts[0].ToolResult.Content
	for _, parts := range [][]Part{userParts, toolParts} {
		if len(parts) != 3 || parts[1].Text.Text != `[Image "shot.png": prepared from 2646x12249 to 442x2048 for model input; coordinates refer to the prepared image]` {
			t.Fatalf("resize notice = %v", parts)
		}
		media := parts[2].Media
		data, err := base64.StdEncoding.DecodeString(media.Source.Data)
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil || img.Bounds().Dx() != 442 || img.Bounds().Dy() != 2048 || len(media.Source.Data) > imageMaxEncodedBytes {
			t.Fatalf("prepared image invalid or outside limits: %v", err)
		}
	}
}

func TestGeneratePreservesSmallImagesAndCorrectsMIME(t *testing.T) {
	t.Parallel()
	part := imageTestPart(t, 4, 3)
	part.Media.MimeType = "image/jpeg"
	req := &Request{Messages: []Message{{Role: RoleUser, Parts: []Part{part}}}}
	probe := &retryTestLLM{}
	for _, err := range Generate(t.Context(), probe, req) {
		if err != nil {
			t.Fatal(err)
		}
	}
	parts := probe.seenReqs[0].Messages[0].Parts
	if len(parts) != 1 || parts[0].Media.Source.Data != part.Media.Source.Data || parts[0].Media.MimeType != "image/png" {
		t.Fatal("small valid image was re-encoded or MIME was not corrected")
	}
	if part.Media.MimeType != "image/jpeg" {
		t.Fatal("canonical MIME was changed")
	}
}

func TestImagePreparationPreservesSupportedFormatsWithinBudget(t *testing.T) {
	t.Parallel()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 120, A: 255})
	var pngBytes, jpegBytes, gifBytes bytes.Buffer
	if err := png.Encode(&pngBytes, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpegBytes, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifBytes, img, nil); err != nil {
		t.Fatal(err)
	}
	// A one-pixel lossless WebP encoded from our PNG fixture with cwebp.
	webpBytes, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA4AAAAvAAAAAAcQEf0PRET/Aw==")
	if err != nil {
		t.Fatal(err)
	}
	for format, data := range map[string][]byte{"png": pngBytes.Bytes(), "jpeg": jpegBytes.Bytes(), "gif": gifBytes.Bytes(), "webp": webpBytes} {
		part := NewMediaPart(MediaModalityImage, MediaSource{Kind: MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data)}, "application/octet-stream", "fixture")
		prepared := prepareInlineImage(part.Media)
		if prepared.media == nil || prepared.note != "" || prepared.media.Source != part.Media.Source || prepared.media.MimeType != "image/"+format {
			t.Fatalf("%s: valid small image was lost, re-encoded, or mislabeled", format)
		}
	}
}

func TestImagePreparationPreservesJPEGDisplayOrientation(t *testing.T) {
	t.Parallel()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 4096))
	for y := 0; y < 4096; y++ {
		pixel := color.NRGBA{R: 255, A: 255}
		if y >= 2048 {
			pixel = color.NRGBA{B: 255, A: 255}
		}
		for x := 0; x < 8; x++ {
			img.SetNRGBA(x, y, pixel)
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	// EXIF orientation 6 displays the raw portrait rotated clockwise.
	app1 := []byte{0xff, 0xe1, 0, 34, 'E', 'x', 'i', 'f', 0, 0,
		'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1,
		1, 18, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 0}
	data := append(append(append([]byte(nil), encoded.Bytes()[:2]...), app1...), encoded.Bytes()[2:]...)
	part := NewMediaPart(MediaModalityImage, MediaSource{Kind: MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data)}, "image/jpeg", "camera.jpg")
	prepared := prepareInlineImage(part.Media)
	if prepared.media == nil {
		t.Fatal(prepared.note)
	}
	decoded, err := base64.StdEncoding.DecodeString(prepared.media.Source.Data)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil || result.Bounds().Dx() != 2048 || result.Bounds().Dy() != 4 {
		t.Fatalf("orientation was lost during resizing: %v", err)
	}
	left := color.NRGBAModel.Convert(result.At(0, 0)).(color.NRGBA)
	right := color.NRGBAModel.Convert(result.At(2047, 0)).(color.NRGBA)
	if left.B < 200 || left.R > 40 || right.R < 200 || right.B > 40 {
		t.Fatalf("prepared image rotated or mirrored incorrectly: left=%v right=%v", left, right)
	}
}

func TestGenerateOmitsInvalidImagesWithoutLosingOtherEvidence(t *testing.T) {
	t.Parallel()
	valid := imageTestPart(t, 1, 1)
	data, err := base64.StdEncoding.DecodeString(valid.Media.Source.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"!not-base64!", base64.StdEncoding.EncodeToString([]byte("not an image")), base64.StdEncoding.EncodeToString(data[:33])} {
		part := valid
		media := *valid.Media
		part.Media = &media
		part.Media.Source.Data = bad
		probe := &retryTestLLM{}
		for _, err := range Generate(t.Context(), probe, &Request{Messages: []Message{{Role: RoleUser, Parts: []Part{NewTextPart("keep"), part, valid}}}}) {
			if err != nil {
				t.Fatal(err)
			}
		}
		parts := probe.seenReqs[0].Messages[0].Parts
		if len(parts) != 3 || parts[0].Text.Text != "keep" || parts[1].Text == nil || !strings.Contains(parts[1].Text.Text, "omitted:") || parts[2].Media == nil {
			t.Fatal("invalid image prevented usable evidence from reaching provider")
		}
	}
}

func TestImagePreparationBoundsDecodeAllocation(t *testing.T) {
	t.Parallel()
	part := imageTestPart(t, 1, 1)
	data, err := base64.StdEncoding.DecodeString(part.Media.Source.Data)
	if err != nil {
		t.Fatal(err)
	}
	// A valid IHDR advertising huge dimensions must not trigger allocation.
	binary.BigEndian.PutUint32(data[16:20], 100000)
	binary.BigEndian.PutUint32(data[20:24], 100000)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	part.Media.Source.Data = base64.StdEncoding.EncodeToString(data)
	prepared := prepareInlineImage(part.Media)
	if prepared.media != nil || !strings.Contains(prepared.note, "pixel limit") {
		t.Fatal("unbounded source was not omitted")
	}
}

func TestImagePreparationBoundsEncodedBytes(t *testing.T) {
	t.Parallel()
	img := image.NewNRGBA(image.Rect(0, 0, 2048, 2048))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < len(img.Pix); i += 4 {
		pixel := rng.Uint32()
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(pixel), byte(pixel>>8), byte(pixel>>16), 255
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	media := NewMediaPart(MediaModalityImage, MediaSource{Kind: MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data.Bytes())}, "image/png", "noise.png").Media
	if len(media.Source.Data) <= imageMaxEncodedBytes {
		t.Fatal("fixture does not exceed encoded byte budget")
	}
	prepared := prepareInlineImage(media)
	if prepared.media == nil || prepared.media.MimeType != "image/jpeg" || len(prepared.media.Source.Data) > imageMaxEncodedBytes {
		t.Fatal("large PNG was not re-encoded within byte budget")
	}
	decoded, err := base64.StdEncoding.DecodeString(prepared.media.Source.Data)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := image.Decode(bytes.NewReader(decoded)); err != nil {
		t.Fatalf("prepared image is not decodable: %v", err)
	}
}

func TestImagePreparationLeavesRemoteReferencesUntouched(t *testing.T) {
	t.Parallel()
	req := &Request{Messages: []Message{{Role: RoleUser, Parts: []Part{
		NewMediaPart(MediaModalityImage, MediaSource{Kind: MediaSourceURL, URI: "https://example.invalid/private.png"}, "image/png", "remote"),
		NewMediaPart(MediaModalityAudio, MediaSource{Kind: MediaSourceInline, Data: "audio"}, "audio/wav", "audio"),
	}}}}
	want := CloneRequest(req)
	if err := prepareRequestImages(t.Context(), req); err != nil || !reflect.DeepEqual(req, want) {
		t.Fatal("image preparation changed remote references or other modalities")
	}
}

func TestImagePreparationCancellationDoesNotDispatch(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	probe := &retryTestLLM{}
	var last error
	for _, err := range Generate(ctx, probe, &Request{}) {
		last = err
	}
	if !errors.Is(last, context.Canceled) || probe.calls != 0 {
		t.Fatalf("error=%v calls=%d", last, probe.calls)
	}
}
