package textstream

import "unicode/utf8"

type UTF8Decoder struct {
	pending []byte
}

func (d *UTF8Decoder) Decode(chunk []byte) string {
	if len(chunk) == 0 {
		return ""
	}
	data := append(append([]byte(nil), d.pending...), chunk...)
	d.pending = nil
	if utf8.Valid(data) {
		return string(data)
	}
	for cut := len(data) - 1; cut >= 0 && len(data)-cut < utf8.UTFMax; cut-- {
		if utf8.Valid(data[:cut]) {
			d.pending = append(d.pending, data[cut:]...)
			return string(data[:cut])
		}
	}
	return string(data)
}

func (d *UTF8Decoder) Flush() string {
	if len(d.pending) == 0 {
		return ""
	}
	text := string(d.pending)
	d.pending = nil
	return text
}
