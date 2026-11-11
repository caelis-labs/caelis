package gojaexec

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

const WorkerArgument = "--code-mode-goja-worker"

const (
	frameConfig byte = iota + 1
	frameSource
	frameReady
	frameCall
	frameReply
	frameResult
	frameError
	frameCancel
	frameBudget
)

type frame struct {
	kind byte
	id   uint64
	data []byte
}

type wireWriter struct {
	mu    sync.Mutex
	w     io.Writer
	bytes atomic.Int64
}

// The fixed header is validated before allocating any payload. Readers use a
// phase-specific ceiling, then validate the per-operation limit before dispatch.
func readFrame(r io.Reader, max int) (frame, error) {
	var header [13]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame{}, err
	}
	n := binary.BigEndian.Uint32(header[:4])
	if uint64(n) > uint64(max) {
		return frame{}, fmt.Errorf("RPC frame exceeds %d bytes", max)
	}
	f := frame{kind: header[4], id: binary.BigEndian.Uint64(header[5:]), data: make([]byte, int(n))}
	_, err := io.ReadFull(r, f.data)
	return f, err
}

func (w *wireWriter) send(kind byte, id uint64, data []byte, max int) error {
	if len(data) > max {
		return fmt.Errorf("RPC payload exceeds %d bytes", max)
	}
	var header [13]byte
	binary.BigEndian.PutUint32(header[:4], uint32(len(data)))
	header[4] = kind
	binary.BigEndian.PutUint64(header[5:], id)
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.w.Write(header[:])
	w.bytes.Add(int64(n))
	if err != nil {
		return err
	}
	n, err = w.w.Write(data)
	w.bytes.Add(int64(n))
	return err
}
