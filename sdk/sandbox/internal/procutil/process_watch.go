package procutil

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"time"
)

var ErrIdleTimeout = errors.New("process idle timeout exceeded")

type ActivityWriter struct {
	buffer     *bytes.Buffer
	lastOutput *atomic.Int64
	stream     string
	onOutput   func(stream string, text string)
}

func NewActivityWriter(buffer *bytes.Buffer, lastOutput *atomic.Int64, stream string, onOutput func(stream string, text string)) *ActivityWriter {
	return &ActivityWriter{
		buffer:     buffer,
		lastOutput: lastOutput,
		stream:     stream,
		onOutput:   onOutput,
	}
}

func (w *ActivityWriter) Write(p []byte) (int, error) {
	if w.lastOutput != nil {
		w.lastOutput.Store(time.Now().UnixNano())
	}
	if w.onOutput != nil && len(p) > 0 {
		w.onOutput(w.stream, string(p))
	}
	if w.buffer == nil {
		return len(p), nil
	}
	return w.buffer.Write(p)
}

func WaitWithIdleTimeout(ctx context.Context, cmd *exec.Cmd, idleTimeout time.Duration, lastOutput *atomic.Int64) error {
	if cmd == nil {
		return errors.New("nil command")
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	if idleTimeout <= 0 {
		select {
		case err := <-waitCh:
			return err
		case <-ctx.Done():
			_ = KillProcess(cmd)
			<-waitCh
			return ctx.Err()
		}
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-waitCh:
			return err
		case <-ctx.Done():
			_ = KillProcess(cmd)
			<-waitCh
			return ctx.Err()
		case <-ticker.C:
			if lastOutput == nil {
				continue
			}
			last := time.Unix(0, lastOutput.Load())
			if time.Since(last) > idleTimeout {
				_ = KillProcess(cmd)
				<-waitCh
				return ErrIdleTimeout
			}
		}
	}
}

func KillProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// Kill the whole process group so child processes (for example spawned by
	// "go run" / shells) do not keep stdout/stderr pipes open.
	if err := KillProcessGroup(cmd.Process.Pid); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
