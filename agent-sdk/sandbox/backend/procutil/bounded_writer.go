package procutil

import (
	"fmt"
	"io"
)

// BoundedWriter prevents a command from retaining unbounded output in memory.
// On overflow it fails the pipe copy; the command owner still drains or cancels
// the process before returning the error.
type BoundedWriter struct {
	Writer    io.Writer
	Remaining int
	Exceeded  bool
}

func (w *BoundedWriter) Write(p []byte) (int, error) {
	if len(p) > w.Remaining {
		w.Exceeded = true
		return 0, fmt.Errorf("sandbox command output budget exceeded")
	}
	n, err := w.Writer.Write(p)
	w.Remaining -= n
	return n, err
}
