package cmdsession

import (
	"bytes"
	"sync"
	"testing"
)

func TestAsyncSessionReadOutputSignalsDescriptorFinal(t *testing.T) {
	var callbacks []AsyncOutputChunk
	session := &AsyncSession{
		doneChan:     make(chan struct{}),
		outputSignal: make(chan struct{}),
		onOutput: func(chunk AsyncOutputChunk) {
			callbacks = append(callbacks, chunk)
		},
	}
	session.readersWg.Add(1)
	session.readOutput(bytes.NewReader([]byte("中文")), "stdout", NewRingBuffer(64))

	if len(callbacks) != 2 || string(callbacks[0].Data) != "中文" || callbacks[0].Final {
		t.Fatalf("output callbacks = %#v, want data followed by final", callbacks)
	}
	if callbacks[0].Cursor != int64(len([]byte("中文"))) {
		t.Fatalf("data callback cursor = %d, want %d", callbacks[0].Cursor, len([]byte("中文")))
	}
	if !callbacks[1].Final || callbacks[1].Stream != "stdout" || len(callbacks[1].Data) != 0 {
		t.Fatalf("final callback = %#v, want empty stdout final marker", callbacks[1])
	}
	if callbacks[1].Cursor != callbacks[0].Cursor {
		t.Fatalf("final callback cursor = %d, want %d", callbacks[1].Cursor, callbacks[0].Cursor)
	}
}

func TestRingBufferReadsRetainedTailDuringWrites(t *testing.T) {
	const capacity = 8
	rb := NewRingBuffer(capacity)
	data := bytes.Repeat([]byte("x"), capacity)
	_, _ = rb.Write(data)
	var group sync.WaitGroup
	group.Go(func() {
		for range 1000 {
			_, _ = rb.Write(data)
		}
	})
	for range 1000 {
		tail, cursor := rb.ReadNewSince(0)
		if !bytes.Equal(tail, data) || cursor%capacity != 0 {
			t.Fatalf("read tail %q at cursor %d", tail, cursor)
		}
	}
	group.Wait()
	tail, cursor := rb.ReadNewSince(0)
	if !bytes.Equal(tail, data) || cursor != 1001*capacity {
		t.Fatalf("final tail %q at %d", tail, cursor)
	}
}
