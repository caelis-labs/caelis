package cmdsession

import (
	"sync"
	"time"
)

// RingBuffer is a thread-safe circular buffer for storing command output.
// It maintains a fixed capacity and discards oldest data when full.
type RingBuffer struct {
	data       []byte
	capacity   int
	writePos   int
	readPos    int
	full       bool
	mu         sync.RWMutex
	lastWrite  time.Time
	totalBytes int64
	dropped    int64
}

// NewRingBuffer creates a new ring buffer with the specified capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 64 * 1024 // 64KB default
	}
	return &RingBuffer{
		data:      make([]byte, capacity),
		capacity:  capacity,
		lastWrite: time.Now(),
	}
}

// Write appends data to the buffer, overwriting oldest data if necessary.
func (rb *RingBuffer) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.lastWrite = time.Now()
	rb.totalBytes += int64(len(p))

	// If input is larger than capacity, only keep the tail
	if len(p) >= rb.capacity {
		rb.dropped += int64(rb.lenLocked() + len(p) - rb.capacity)
		copy(rb.data, p[len(p)-rb.capacity:])
		rb.writePos = 0
		rb.readPos = 0
		rb.full = true
		return len(p), nil
	}

	for i := 0; i < len(p); i++ {
		// Check if we're about to overwrite unread data
		if rb.full && rb.writePos == rb.readPos {
			rb.dropped++
			rb.readPos = (rb.readPos + 1) % rb.capacity
		}

		rb.data[rb.writePos] = p[i]
		rb.writePos = (rb.writePos + 1) % rb.capacity

		if rb.writePos == rb.readPos {
			rb.full = true
		}
	}

	return len(p), nil
}

// ReadAll returns all unread data without consuming it.
func (rb *RingBuffer) ReadAll() []byte {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	return rb.readAllLocked()
}

func (rb *RingBuffer) readAllLocked() []byte {
	if rb.writePos == rb.readPos && !rb.full {
		return nil
	}

	var result []byte
	if rb.full || rb.writePos < rb.readPos {
		// Buffer wrapped around
		result = make([]byte, rb.capacity)
		copy(result, rb.data[rb.readPos:])
		copy(result[rb.capacity-rb.readPos:], rb.data[:rb.writePos])
		if !rb.full {
			result = result[:rb.capacity-rb.readPos+rb.writePos]
		}
	} else {
		result = make([]byte, rb.writePos-rb.readPos)
		copy(result, rb.data[rb.readPos:rb.writePos])
	}

	return result
}

// ReadAndConsume returns all unread data and marks it as consumed.
func (rb *RingBuffer) ReadAndConsume() []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	result := rb.readAllLocked()
	rb.readPos = rb.writePos
	rb.full = false
	return result
}

// ReadNewSince returns data written after the given position marker.
// Returns the data and a new position marker.
func (rb *RingBuffer) ReadNewSince(marker int64) ([]byte, int64) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	currentTotal := rb.totalBytes
	if marker >= currentTotal {
		return nil, currentTotal
	}

	// Calculate how much new data is available
	newBytes := currentTotal - marker
	if newBytes > int64(rb.capacity) {
		// Some data was lost due to wrap-around
		newBytes = int64(rb.Len())
	}

	data := rb.readAllLocked()
	if int64(len(data)) > newBytes {
		data = data[len(data)-int(newBytes):]
	}

	return data, currentTotal
}

// Len returns the current amount of data in the buffer.
func (rb *RingBuffer) Len() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	return rb.lenLocked()
}

func (rb *RingBuffer) lenLocked() int {
	if rb.full {
		return rb.capacity
	}
	if rb.writePos >= rb.readPos {
		return rb.writePos - rb.readPos
	}
	return rb.capacity - rb.readPos + rb.writePos
}

// Cap returns the buffer capacity.
func (rb *RingBuffer) Cap() int {
	return rb.capacity
}

// TotalWritten returns the total bytes written since creation.
func (rb *RingBuffer) TotalWritten() int64 {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.totalBytes
}

// DroppedBytes returns the total number of bytes discarded because the buffer
// capacity was exceeded.
func (rb *RingBuffer) DroppedBytes() int64 {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.dropped
}

// EarliestMarker returns the earliest total-bytes marker whose data is still retained.
func (rb *RingBuffer) EarliestMarker() int64 {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.totalBytes - int64(rb.lenLocked())
}

// LastWriteTime returns the timestamp of the last write operation.
func (rb *RingBuffer) LastWriteTime() time.Time {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.lastWrite
}

// Reset clears the buffer.
func (rb *RingBuffer) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.writePos = 0
	rb.readPos = 0
	rb.full = false
	rb.totalBytes = 0
	rb.dropped = 0
	rb.lastWrite = time.Now()
}
