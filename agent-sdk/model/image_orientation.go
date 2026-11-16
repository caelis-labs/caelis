package model

import (
	"bytes"
	"encoding/binary"
	"image"
)

// jpegOrientation reads only the EXIF orientation needed for image preparation.
// Invalid or absent metadata leaves the decoded pixels in their original order.
// Every segment and TIFF offset is bounded by its containing metadata block.
func jpegOrientation(data []byte) int {
	if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
		return 1
	}
	data = data[2:]
	for len(data) >= 2 && data[0] == 0xff {
		if data[1] == 0xff { // JPEG permits fill bytes before a marker.
			data = data[1:]
			continue
		}
		marker := data[1]
		data = data[2:]
		switch {
		case marker == 0xda || marker == 0xd9: // Stop before entropy-coded pixels.
			return 1
		case marker == 0x01 || marker >= 0xd0 && marker <= 0xd7:
			continue // Standalone markers have no segment length.
		}
		if len(data) < 2 {
			return 1
		}
		size := int(binary.BigEndian.Uint16(data))
		if size < 2 || size > len(data) {
			return 1
		}
		segment := data[2:size]
		if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return exifOrientation(segment[6:])
		}
		data = data[size:]
	}
	return 1
}

func exifOrientation(data []byte) int {
	if len(data) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(data[2:4]) != 42 {
		return 1
	}
	offset := uint64(order.Uint32(data[4:8]))
	if offset < 8 || offset+2 > uint64(len(data)) {
		return 1
	}
	count := int(order.Uint16(data[offset : offset+2]))
	entries := data[offset+2:]
	if count > len(entries)/12 {
		return 1
	}
	for i := 0; i < count; i++ {
		entry := entries[i*12 : (i+1)*12]
		if order.Uint16(entry[:2]) != 0x0112 {
			continue
		}
		// Orientation is one SHORT stored inline, never an external offset.
		if order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return 1
		}
		orientation := int(order.Uint16(entry[8:10]))
		if orientation >= 1 && orientation <= 8 {
			return orientation
		}
		return 1
	}
	return 1
}

// orientJPEGImage applies the eight EXIF display orientations before cropping.
// It copies decoded pixels without resampling; scaling and encoding stay with
// encodePreparedImage. Images that already have normal orientation are reused.
func orientJPEGImage(src image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return src
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if orientation >= 5 {
		w, h = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx, sy := x, y
			switch orientation {
			case 2:
				sx = w - 1 - x
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sy = h - 1 - y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, w-1-x
			case 7:
				sx, sy = h-1-y, w-1-x
			case 8:
				sx, sy = h-1-y, x
			}
			dst.Set(x, y, src.At(bounds.Min.X+sx, bounds.Min.Y+sy))
		}
	}
	return dst
}
