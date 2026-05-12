package tuiapp

import "strings"

type logChunkBuffer struct {
	chunks []string
	bytes  int
}

func (b *logChunkBuffer) Append(chunk string) bool {
	if chunk == "" {
		return false
	}
	b.chunks = append(b.chunks, chunk)
	b.bytes += len(chunk)
	return true
}

func (b *logChunkBuffer) Empty() bool {
	return b == nil || len(b.chunks) == 0
}

func (b *logChunkBuffer) Drain() string {
	if b == nil || len(b.chunks) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.Grow(b.bytes)
	for _, chunk := range b.chunks {
		builder.WriteString(chunk)
	}
	out := builder.String()
	b.chunks = b.chunks[:0]
	b.bytes = 0
	return out
}

type logStreamBuffer struct {
	tail strings.Builder
}

func (b *logStreamBuffer) Append(chunk string) []string {
	if b == nil || chunk == "" {
		return nil
	}
	var lines []string
	for chunk != "" {
		idx := strings.IndexByte(chunk, '\n')
		if idx < 0 {
			b.tail.WriteString(chunk)
			break
		}
		part := chunk[:idx]
		if b.tail.Len() == 0 {
			lines = append(lines, part)
		} else {
			b.tail.WriteString(part)
			lines = append(lines, b.tail.String())
			b.tail.Reset()
		}
		chunk = chunk[idx+1:]
	}
	return lines
}

func (b *logStreamBuffer) Tail() string {
	if b == nil || b.tail.Len() == 0 {
		return ""
	}
	return b.tail.String()
}

func (b *logStreamBuffer) Reset() {
	if b == nil {
		return
	}
	b.tail.Reset()
}
