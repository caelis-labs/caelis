package client

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// DefaultProcessExitGrace is the opportunity an owned ACP process gets to
// exit after its protocol input closes before cleanup escalates to forced
// process-tree termination.
const DefaultProcessExitGrace = 5 * time.Second

// CloseStage identifies which proof-bearing part of ACP client cleanup failed.
type CloseStage string

const (
	// CloseStageConnectionClose means the protocol streams could not be closed.
	CloseStageConnectionClose CloseStage = "connection_close"
	// CloseStageProcessCleanup means forced process-tree termination or handle
	// release failed after the process had been asked to exit.
	CloseStageProcessCleanup CloseStage = "process_cleanup"
	// CloseStageConnectionJoin means connection-owned goroutines did not reach
	// their terminal state within the cleanup window.
	CloseStageConnectionJoin CloseStage = "connection_join"
)

// CloseError reports a cleanup failure at one explicit ACP client close stage.
// A process graceful-exit deadline returned by Process.Shutdown is not itself a
// CloseError: Shutdown has already forcefully terminated and joined the owned
// process tree before it returns that deadline cause.
type CloseError struct {
	Stage CloseStage
	Err   error
}

func (e *CloseError) Error() string {
	if e == nil {
		return "acp client close failed"
	}
	if e.Err == nil {
		return fmt.Sprintf("acp client close failed at %s", e.Stage)
	}
	return fmt.Sprintf("acp client close failed at %s: %v", e.Stage, e.Err)
}

func (e *CloseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Close shuts down an owned process within the caller's cleanup window, then
// closes the protocol connection and joins its goroutines. Process cleanup
// escalates to joined process-tree termination when that window expires. If ctx
// has no deadline, DefaultProcessExitGrace supplies the window. The first call
// owns the terminal result returned by later calls.
func (c *Client) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.closeErr = c.close(ctx)
	})
	return c.closeErr
}

func (c *Client) close(ctx context.Context) error {
	ctx, cancel := processExitContext(ctx)
	defer cancel()
	defer c.releaseEndpoint()

	var errs []error
	if c.proc != nil {
		// On Windows, closing an os.File that has a blocked pipe read waits for
		// that read to finish. Closing the SDK connection first would therefore
		// block on child stdout before the process owner could close child stdin.
		// Shut the owned process down first: its input closes immediately and its
		// terminal output closes the receive side before Connection.Close joins it.
		shutdownErr := completeProcessShutdownError(ctx, c.proc.Shutdown(ctx), func() error {
			// Process.Shutdown's forced path has already joined waitDone before
			// returning the context cause, so this observes the terminal process
			// and process-tree release result without extending the close window.
			return c.proc.Wait(context.Background())
		})
		if shutdownErr != nil {
			errs = append(errs, &CloseError{Stage: CloseStageProcessCleanup, Err: shutdownErr})
		}
	}
	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			errs = append(errs, &CloseError{Stage: CloseStageConnectionClose, Err: err})
		}
		joinCtx := ctx
		joinCancel := func() {}
		if c.proc != nil && context.Cause(ctx) != nil {
			// Process.Shutdown may consume the caller's grace while forcefully
			// terminating and joining the process tree. Once that proof exists,
			// give the now-closed transport a bounded detached window to publish
			// its connection-goroutine join instead of selecting the stale cause.
			joinCtx, joinCancel = context.WithTimeout(context.WithoutCancel(ctx), DefaultProcessExitGrace)
		}
		// Join through the bounded cleanup context. Discovery starts detached;
		// forced owned-process cleanup may use the detached terminal window above.
		if err := c.conn.Wait(joinCtx); context.Cause(joinCtx) != nil && errors.Is(err, context.Cause(joinCtx)) {
			errs = append(errs, &CloseError{Stage: CloseStageConnectionJoin, Err: context.Cause(joinCtx)})
		}
		joinCancel()
		// Any non-context Wait result proves the connection goroutines joined.
		// The operation that observed its transport cause owns the reporting; it
		// is not a cleanup failure.
	}
	return errors.Join(errs...)
}

func processExitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, DefaultProcessExitGrace)
}

func unexpectedProcessShutdownError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	return filterProvenProcessTermination(err, context.Cause(ctx))
}

func completeProcessShutdownError(ctx context.Context, shutdownErr error, wait func() error) error {
	gracefulCause := context.Cause(ctx)
	if gracefulCause != nil && errors.Is(shutdownErr, gracefulCause) && wait != nil {
		// The SDK's forced Shutdown branch omits waitErr, where process-tree
		// handle release failures are retained. Observe it after Shutdown has
		// joined the waiter before accepting the grace expiry as proven cleanup.
		shutdownErr = errors.Join(shutdownErr, wait())
	}
	return unexpectedProcessShutdownError(ctx, shutdownErr)
}

type joinedErrors interface {
	Unwrap() []error
}

func filterProvenProcessTermination(err error, gracefulCause error) error {
	if err == nil {
		return nil
	}
	var joined joinedErrors
	if errors.As(err, &joined) {
		unexpected := make([]error, 0, len(joined.Unwrap()))
		for _, child := range joined.Unwrap() {
			if child = filterProvenProcessTermination(child, gracefulCause); child != nil {
				unexpected = append(unexpected, child)
			}
		}
		return errors.Join(unexpected...)
	}
	if gracefulCause != nil && errors.Is(err, gracefulCause) {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A non-zero graceful exit can be diagnostically interesting, but the
		// owned process is terminal and its cleanup is proven.
		return nil
	}
	return err
}

func (c *Client) releaseEndpoint() {
	if c == nil {
		return
	}
	c.releaseOnce.Do(func() {
		if c.release != nil {
			c.release()
		}
	})
}
