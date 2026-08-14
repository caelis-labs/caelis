package consoleoutput

import "sync"

// StreamStorageMode controls which bytes become the durable session buffer.
type StreamStorageMode int

const (
	// StoreRaw preserves process pipe bytes for Result()/ReadOutput callers.
	StoreRaw StreamStorageMode = iota
	// StoreDecoded stores the same normalized UTF-8 text emitted live.
	StoreDecoded
)

// StreamChunk is one decoded pipe chunk split into durable and live forms.
type StreamChunk struct {
	Stored []byte
	Emit   []byte
}

// DecodeStreamChunk normalizes one process pipe chunk for live terminal output
// and chooses whether session storage keeps raw pipe bytes or normalized text.
func DecodeStreamChunk(decoder *ConsoleOutputDecoder, chunk []byte, mode StreamStorageMode) StreamChunk {
	if decoder == nil {
		stored := append([]byte(nil), chunk...)
		return StreamChunk{Stored: stored, Emit: stored}
	}
	emit := decoder.Decode(chunk)
	stored := emit
	if mode == StoreRaw {
		stored = append([]byte(nil), chunk...)
	}
	return StreamChunk{Stored: stored, Emit: emit}
}

// FlushStreamChunk emits pending decoder text at process exit. Raw-storage
// callers have already stored all raw pipe bytes during DecodeStreamChunk, so
// only decoded-storage callers receive a Stored tail.
func FlushStreamChunk(decoder *ConsoleOutputDecoder, mode StreamStorageMode) StreamChunk {
	if decoder == nil {
		return StreamChunk{}
	}
	emit := decoder.Flush()
	if mode == StoreDecoded {
		return StreamChunk{Stored: emit, Emit: emit}
	}
	return StreamChunk{Emit: emit}
}

// CappedBuffer stores decoded Windows console output up to a byte cap.
type CappedBuffer struct {
	mu      sync.Mutex
	max     int
	buf     []byte
	decoder ConsoleOutputDecoder
	flushed bool
}

// NewCappedBuffer returns an io.Writer that decodes console output before
// storing it.
func NewCappedBuffer(max int) *CappedBuffer {
	return &CappedBuffer{max: max}
}

func (b *CappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	chunk := DecodeStreamChunk(&b.decoder, p, StoreDecoded)
	if len(chunk.Stored) > 0 {
		b.buf = AppendCappedBytes(b.buf, chunk.Stored, b.max)
	}
	return len(p), nil
}

func (b *CappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.flushed {
		if tail := FlushStreamChunk(&b.decoder, StoreDecoded); len(tail.Stored) > 0 {
			b.buf = AppendCappedBytes(b.buf, tail.Stored, b.max)
		}
		b.flushed = true
	}
	return string(b.buf)
}

// RawCappedBuffer stores process pipe bytes up to a byte cap.
type RawCappedBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

// NewRawCappedBuffer returns an io.Writer that stores raw process pipe bytes.
func NewRawCappedBuffer(max int) *RawCappedBuffer {
	return &RawCappedBuffer{max: max}
}

func (b *RawCappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = AppendCappedBytes(b.buf, p, b.max)
	return len(p), nil
}

func (b *RawCappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// AppendCappedBytes appends src while retaining only the last max bytes.
func AppendCappedBytes(dst []byte, src []byte, max int) []byte {
	if max <= 0 {
		return append(dst, src...)
	}
	if len(src) >= max {
		return append([]byte(nil), src[len(src)-max:]...)
	}
	keep := max - len(src)
	if len(dst) > keep {
		dst = dst[len(dst)-keep:]
	}
	out := append([]byte(nil), dst...)
	return append(out, src...)
}

// CappedOutputSince returns retained bytes after marker and the new absolute
// cursor. Session callers use sandbox.OutputReadWindow on the returned byte
// length and cursor to distinguish a contiguous read from ring eviction.
func CappedOutputSince(buf []byte, total int64, marker int64) ([]byte, int64) {
	if total < 0 {
		total = 0
	}
	base := total - int64(len(buf))
	if base < 0 {
		base = 0
	}
	if marker < base {
		marker = base
	}
	if marker > total {
		marker = total
	}
	start := marker - base
	if start < 0 {
		start = 0
	}
	if start > int64(len(buf)) {
		start = int64(len(buf))
	}
	return append([]byte(nil), buf[start:]...), total
}
