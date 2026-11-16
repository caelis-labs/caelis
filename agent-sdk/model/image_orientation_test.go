package model

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
)

func jpegOrientationSegment(orientation int, order binary.ByteOrder) []byte {
	segment := make([]byte, 36)
	copy(segment, []byte{0xff, 0xe1, 0, 34, 'E', 'x', 'i', 'f', 0, 0})
	tiff := segment[10:]
	copy(tiff, "MM")
	if order == binary.LittleEndian {
		copy(tiff, "II")
	}
	order.PutUint16(tiff[2:4], 42)
	order.PutUint32(tiff[4:8], 8)
	order.PutUint16(tiff[8:10], 1)
	order.PutUint16(tiff[10:12], 0x0112)
	order.PutUint16(tiff[12:14], 3)
	order.PutUint32(tiff[14:18], 1)
	order.PutUint16(tiff[18:20], uint16(orientation))
	return segment
}

func TestImagePreparationPreservesEveryJPEGOrientation(t *testing.T) {
	t.Parallel()
	colors := []color.NRGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}, {R: 255, G: 255, A: 255}}
	src := image.NewNRGBA(image.Rect(0, 0, 2056, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 2056; x++ {
			src.SetNRGBA(x, y, colors[(y/512)*2+x/1028])
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	// Expected displayed corners: top-left, top-right, bottom-left, bottom-right.
	corners := [][4]int{{0, 1, 2, 3}, {1, 0, 3, 2}, {3, 2, 1, 0}, {2, 3, 0, 1}, {0, 2, 1, 3}, {2, 0, 3, 1}, {3, 1, 2, 0}, {1, 3, 0, 2}}
	for _, order := range []binary.ByteOrder{binary.BigEndian, binary.LittleEndian} {
		for i, want := range corners {
			t.Run(fmt.Sprintf("%s/%d", order, i+1), func(t *testing.T) {
				data := bytes.Join([][]byte{encoded.Bytes()[:2], jpegOrientationSegment(i+1, order), encoded.Bytes()[2:]}, nil)
				part := NewMediaPart(MediaModalityImage, MediaSource{Kind: MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data)}, "image/jpeg", "camera.jpg")
				prepared := prepareInlineImage(part.Media)
				if len(prepared.images) != 1 || prepared.images[0].mimeType != "image/png" {
					t.Fatalf("unexpected preparation: %+v", prepared)
				}
				raw, err := base64.StdEncoding.DecodeString(prepared.images[0].data)
				if err != nil {
					t.Fatal(err)
				}
				decoded, _, err := image.Decode(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				w, h := 2048, 1020
				if i+1 >= 5 {
					w, h = h, w
				}
				if decoded.Bounds() != image.Rect(0, 0, w, h) {
					t.Fatalf("display dimensions = %v", decoded.Bounds())
				}
				for j, point := range []image.Point{{0, 0}, {w - 1, 0}, {0, h - 1}, {w - 1, h - 1}} {
					got := color.NRGBAModel.Convert(decoded.At(point.X, point.Y)).(color.NRGBA)
					expect := colors[want[j]]
					for _, channel := range [][2]byte{{got.R, expect.R}, {got.G, expect.G}, {got.B, expect.B}, {got.A, expect.A}} {
						if delta := int(channel[0]) - int(channel[1]); delta < -5 || delta > 5 {
							t.Fatalf("display corner %v = %v, want %v", point, got, expect)
						}
					}
				}
			})
		}
	}
}

func TestImagePreparationPreservesSmallOrientedJPEGBytes(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewGray(image.Rect(0, 0, 8, 4)), nil); err != nil {
		t.Fatal(err)
	}
	data := bytes.Join([][]byte{encoded.Bytes()[:2], jpegOrientationSegment(6, binary.BigEndian), encoded.Bytes()[2:]}, nil)
	part := NewMediaPart(MediaModalityImage, MediaSource{Kind: MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data)}, "image/jpeg", "small.jpg")
	probe := &retryTestLLM{}
	for _, err := range Generate(t.Context(), probe, &Request{Messages: []Message{NewMessage(RoleUser, part)}}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := probe.seenReqs[0].Messages[0].Parts[0].Media
	if got.Source.Data != part.Media.Source.Data || got.MimeType != "image/jpeg" {
		t.Fatal("small JPEG lost its original bytes or EXIF metadata")
	}
}

func TestImagePreparationMalformedJPEGOrientation(t *testing.T) {
	t.Parallel()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewGray(image.Rect(0, 0, 2056, 2)), nil); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte){
		"IFD offset outside APP1": func(s []byte) { binary.BigEndian.PutUint32(s[14:18], 0xffffffff) },
		"IFD before header":       func(s []byte) { binary.BigEndian.PutUint32(s[14:18], 0) },
		"too many tags":           func(s []byte) { binary.BigEndian.PutUint16(s[18:20], 0xffff) },
		"wrong TIFF magic":        func(s []byte) { s[13] = 0 },
		"unknown byte order":      func(s []byte) { s[10] = 'X' },
		"wrong tag type":          func(s []byte) { s[23] = 4 },
		"multiple values":         func(s []byte) { s[27] = 2 },
		"zero orientation":        func(s []byte) { s[29] = 0 },
		"invalid orientation":     func(s []byte) { s[29] = 9 },
		"missing orientation":     func(s []byte) { s[21] = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			segment := jpegOrientationSegment(6, binary.BigEndian)
			mutate(segment)
			data := bytes.Join([][]byte{encoded.Bytes()[:2], segment, encoded.Bytes()[2:]}, nil)
			if got := jpegOrientation(data); got != 1 {
				t.Fatalf("malformed metadata changed orientation to %d", got)
			}
			part := NewMediaPart(MediaModalityImage, MediaSource{Kind: MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data)}, "image/jpeg", "bad-exif.jpg")
			prepared := prepareInlineImage(part.Media)
			if len(prepared.images) != 2 {
				t.Fatalf("malformed optional metadata yielded %d crops, note=%s", len(prepared.images), prepared.note)
			}
			for _, crop := range prepared.images {
				if !strings.Contains(crop.note, "of 2056x2 in display orientation") {
					t.Fatalf("malformed optional metadata rotated usable pixels: %s", crop.note)
				}
			}
		})
	}
}

func FuzzJPEGOrientation(f *testing.F) {
	for _, order := range []binary.ByteOrder{binary.BigEndian, binary.LittleEndian} {
		f.Add(append([]byte{0xff, 0xd8}, jpegOrientationSegment(6, order)...))
	}
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe1, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if got := jpegOrientation(data); got < 1 || got > 8 {
			t.Fatalf("invalid display orientation %d", got)
		}
	})
}

func TestJPEGOrientationBoundsMetadataReads(t *testing.T) {
	segment := jpegOrientationSegment(6, binary.BigEndian)
	data := append([]byte{0xff, 0xd8}, segment...)
	for i := 0; i < len(data); i++ {
		if got := jpegOrientation(data[:i]); got != 1 {
			t.Fatalf("truncated metadata at %d yielded orientation %d", i, got)
		}
	}
	// An IFD outside APP1 must not read into a following JPEG segment.
	short := bytes.Clone(data)
	binary.BigEndian.PutUint16(short[4:6], 16)
	if got := jpegOrientation(short); got != 1 {
		t.Fatalf("orientation %d escaped its APP1 segment", got)
	}
	for _, invalid := range [][]byte{
		{0xff, 0xd8, 0xff, 0xe1, 0, 0},
		{0xff, 0xd8, 0xff, 0xe1, 0, 1},
		{0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff},
		bytes.Join([][]byte{{0xff, 0xd8, 0xff, 0xda, 0, 2}, segment}, nil),
		bytes.Join([][]byte{{0xff, 0xd8, 0xff, 0xd9}, segment}, nil),
	} {
		if got := jpegOrientation(invalid); got != 1 {
			t.Fatalf("invalid JPEG metadata yielded orientation %d", got)
		}
	}
	// Fill bytes, standalone markers and unrelated APP1 blocks have no EXIF.
	prefix := []byte{0xff, 0xd8, 0xff, 0xff, 0x01, 0xff, 0xe1, 0, 5, 'x', 'm', 'p'}
	if got := jpegOrientation(append(prefix, segment...)); got != 6 {
		t.Fatalf("orientation after unrelated markers = %d", got)
	}
}
