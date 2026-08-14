package textstream

import (
	"strings"
	"unicode/utf8"
)

type UTF8Decoder struct {
	pending []byte
}

// PendingBytes reports raw bytes not yet represented by decoded output.
func (d *UTF8Decoder) PendingBytes() int {
	if d == nil {
		return 0
	}
	return len(d.pending)
}

// Reset discards an incomplete prefix when the upstream byte stream reports a
// retention gap. Bytes on opposite sides of a gap must never be decoded as one
// rune.
func (d *UTF8Decoder) Reset() {
	if d == nil {
		return
	}
	d.pending = nil
}

func (d *UTF8Decoder) Decode(chunk []byte) string {
	if len(chunk) == 0 {
		return ""
	}
	data := append(append([]byte(nil), d.pending...), chunk...)
	d.pending = nil
	var out strings.Builder
	out.Grow(len(data))
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			d.pending = append(d.pending, data...)
			break
		}
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			out.WriteRune(utf8.RuneError)
			data = data[1:]
			continue
		}
		out.Write(data[:size])
		data = data[size:]
	}
	return out.String()
}

func (d *UTF8Decoder) Flush() string {
	if len(d.pending) == 0 {
		return ""
	}
	text := strings.ToValidUTF8(string(d.pending), string(utf8.RuneError))
	d.pending = nil
	return text
}
