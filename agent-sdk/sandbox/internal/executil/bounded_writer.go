package executil

import (
	"io"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

// BoundedWriter prevents a command from retaining unbounded output in memory.
// On overflow it fails the pipe copy; the command owner still drains or cancels
// the process before returning the error.
type BoundedWriter struct {
	Writer    io.Writer
	Remaining int
	Exceeded  bool
	OnLimit   func()
}

func (w *BoundedWriter) Write(p []byte) (int, error) {
	if len(p) > w.Remaining {
		w.Exceeded = true
		if w.OnLimit != nil {
			w.OnLimit()
		}
		return 0, sandbox.ErrCommandOutputLimit
	}
	n, err := w.Writer.Write(p)
	w.Remaining -= n
	return n, err
}
