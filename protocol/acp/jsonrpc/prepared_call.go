package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// DispatchError reports whether a JSON-RPC request may have reached the peer.
// WriteStarted is true after dispatch invoked the transport writer. AnyWritten
// is true after at least one request byte was accepted. Either fact makes an
// incomplete dispatch an unknown remote outcome.
type DispatchError struct {
	Cause        error
	WriteStarted bool
	AnyWritten   bool
}

func (e *DispatchError) Error() string {
	if e == nil || e.Cause == nil {
		return "acp/jsonrpc: request dispatch failed"
	}
	return fmt.Sprintf("acp/jsonrpc: request dispatch failed: %v", e.Cause)
}

func (e *DispatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ResponseDecodeError reports that the peer returned a successful JSON-RPC
// response whose result could not be decoded into the requested typed shape.
// The request was fully written and may already have produced a remote effect;
// callers must not turn this local compatibility failure into a retry.
type ResponseDecodeError struct {
	Cause error
}

func (e *ResponseDecodeError) Error() string {
	if e == nil || e.Cause == nil {
		return "acp/jsonrpc: decode successful response result"
	}
	return fmt.Sprintf("acp/jsonrpc: decode successful response result: %v", e.Cause)
}

func (e *ResponseDecodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// DispatchMayHaveCommitted reports whether a call may have reached or been
// applied by the peer. Even a zero-progress write after writer invocation, or
// a successful response with an undecodable typed result, is ambiguous:
// callers must isolate the transport instead of retrying the request.
func DispatchMayHaveCommitted(err error) bool {
	var dispatchErr *DispatchError
	if errors.As(err, &dispatchErr) && (dispatchErr.WriteStarted || dispatchErr.AnyWritten) {
		return true
	}
	var decodeErr *ResponseDecodeError
	return errors.As(err, &decodeErr)
}

type preparedCallState uint8

const (
	preparedCallReady preparedCallState = iota
	preparedCallDispatching
	preparedCallDispatched
	preparedCallFinished
)

// PreparedCall owns one pre-encoded and registered JSON-RPC call. Dispatch and
// Wait are separate so a long-lived producer can take over response ownership
// after the complete request frame is written.
type PreparedCall struct {
	conn    *Conn
	id      int64
	pending *pendingCall
	payload []byte

	writeStarted chan struct{}
	startOnce    sync.Once

	mu    sync.Mutex
	state preparedCallState
}

// PrepareCall encodes and registers a call without touching the transport.
func (c *Conn) PrepareCall(method string, params any) (*PreparedCall, error) {
	if c == nil {
		return nil, fmt.Errorf("acp/jsonrpc: conn is nil")
	}
	methodRaw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := c.nextID.Add(1)
	message := Message{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  method,
		Params:  methodRaw,
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	pending := &pendingCall{ch: make(chan pendingCallResult, 1)}
	c.pending.Store(id, pending)
	return &PreparedCall{
		conn: c, id: id, pending: pending, payload: payload,
		writeStarted: make(chan struct{}),
	}, nil
}

// WriteStarted closes immediately before the first transport Write call. A
// caller waiting for writer admission may cancel without this channel closing.
func (c *PreparedCall) WriteStarted() <-chan struct{} {
	if c == nil || c.writeStarted == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return c.writeStarted
}

// ObserveResponse installs one short, non-blocking callback that runs in the
// connection reader before the response becomes visible to Wait. It lets a
// typed client adapter establish local admission state at the same linearized
// boundary where it recognizes a peer response. The observer must be installed
// before Dispatch.
func (c *PreparedCall) ObserveResponse(observer func(Message) error) error {
	if c == nil || c.pending == nil {
		return fmt.Errorf("acp/jsonrpc: prepared call is unavailable")
	}
	c.mu.Lock()
	if c.state != preparedCallReady {
		c.mu.Unlock()
		return fmt.Errorf("acp/jsonrpc: response observer must be installed before dispatch")
	}
	c.pending.mu.Lock()
	if c.pending.resolved {
		c.pending.mu.Unlock()
		c.mu.Unlock()
		return fmt.Errorf("acp/jsonrpc: prepared call response was already resolved")
	}
	c.pending.responseObserver = observer
	c.pending.mu.Unlock()
	c.mu.Unlock()
	return nil
}

// Dispatch writes the complete prepared request frame. It is safe to call
// exactly once. Cancellation while waiting for writer admission writes no bytes
// and removes the pending call; cancellation after WriteStarted requires the
// transport owner to close the writer to unblock it.
func (c *PreparedCall) Dispatch(ctx context.Context) error {
	return c.dispatch(ctx, nil)
}

// DispatchWithAbort dispatches a prepared call and invokes abort when caller
// cancellation wins after transport writing has started but before the full
// frame is owned by the response waiter. abort must revoke that exact
// transport so a blocked Write returns. Cancellation before write admission
// never invokes abort and remains a proven no-effect failure.
func (c *PreparedCall) DispatchWithAbort(ctx context.Context, abort func()) error {
	return c.dispatch(ctx, abort)
}

func (c *PreparedCall) dispatch(ctx context.Context, abort func()) error {
	if c == nil || c.conn == nil || c.pending == nil {
		return fmt.Errorf("acp/jsonrpc: prepared call is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.state != preparedCallReady {
		c.mu.Unlock()
		return fmt.Errorf("acp/jsonrpc: prepared call was already dispatched")
	}
	c.state = preparedCallDispatching
	c.mu.Unlock()

	var lifecycle dispatchLifecycle
	lifecycle.ensureDone()
	watchDone := make(chan struct{})
	if abort != nil {
		go func() {
			defer close(watchDone)
			select {
			case <-ctx.Done():
				lifecycle.mu.Lock()
				if lifecycle.finished {
					lifecycle.mu.Unlock()
					return
				}
				if !lifecycle.started {
					lifecycle.cancelledBeforeStart = true
					lifecycle.mu.Unlock()
					return
				}
				lifecycle.aborting = true
				lifecycle.mu.Unlock()
				abort()
			case <-lifecycle.done:
			}
		}()
	} else {
		close(watchDone)
	}

	writeStarted := false
	written, err := c.conn.writeEncoded(ctx, c.payload, func() error {
		lifecycle.mu.Lock()
		defer lifecycle.mu.Unlock()
		if lifecycle.cancelledBeforeStart {
			return ctx.Err()
		}
		lifecycle.started = true
		writeStarted = true
		c.startOnce.Do(func() { close(c.writeStarted) })
		return nil
	})
	lifecycle.mu.Lock()
	lifecycle.finished = true
	aborting := lifecycle.aborting
	close(lifecycle.done)
	lifecycle.mu.Unlock()
	<-watchDone
	c.mu.Lock()
	if err == nil {
		c.state = preparedCallDispatched
	} else {
		c.state = preparedCallFinished
	}
	c.mu.Unlock()
	if err != nil {
		c.conn.pending.Delete(c.id)
		return &DispatchError{Cause: err, WriteStarted: writeStarted || aborting, AnyWritten: written > 0}
	}
	if aborting {
		c.conn.pending.Delete(c.id)
		cause := ctx.Err()
		if cause == nil {
			cause = context.Canceled
		}
		return &DispatchError{Cause: cause, WriteStarted: true, AnyWritten: written > 0}
	}
	return nil
}

type dispatchLifecycle struct {
	mu                   sync.Mutex
	done                 chan struct{}
	started              bool
	finished             bool
	aborting             bool
	cancelledBeforeStart bool
}

func (l *dispatchLifecycle) ensureDone() {
	if l.done == nil {
		l.done = make(chan struct{})
	}
}

// Wait waits for the response under its current owner context and then releases
// pending-call ownership.
func (c *PreparedCall) Wait(ctx context.Context, out any) error {
	if c == nil || c.conn == nil || c.pending == nil {
		return fmt.Errorf("acp/jsonrpc: prepared call is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.state != preparedCallDispatched {
		c.mu.Unlock()
		return fmt.Errorf("acp/jsonrpc: prepared call was not dispatched")
	}
	c.mu.Unlock()
	defer func() {
		c.conn.pending.Delete(c.id)
		c.mu.Lock()
		c.state = preparedCallFinished
		c.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case completed := <-c.pending.ch:
		if completed.err != nil {
			return completed.err
		}
		resp := completed.message
		if resp.Error != nil {
			return FormatRPCError(resp.Error)
		}
		if out == nil {
			return nil
		}
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			return &ResponseDecodeError{Cause: err}
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return &ResponseDecodeError{Cause: err}
		}
		return nil
	}
}

// Abandon releases an unwritten or externally isolated call.
func (c *PreparedCall) Abandon() {
	if c == nil || c.conn == nil {
		return
	}
	c.conn.pending.Delete(c.id)
	c.mu.Lock()
	c.state = preparedCallFinished
	c.mu.Unlock()
}

func (c *Conn) writeEncoded(ctx context.Context, payload []byte, onWriteStart func() error) (int, error) {
	if c == nil || c.writer == nil {
		return 0, fmt.Errorf("acp/jsonrpc: writer is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	writes := c.writes
	if writes == nil {
		return 0, fmt.Errorf("acp/jsonrpc: writer arbiter is unavailable")
	}
	release, err := writes.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	total := 0
	for total < len(payload) {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if onWriteStart != nil {
			if err := onWriteStart(); err != nil {
				return total, err
			}
			onWriteStart = nil
		}
		n, writeErr := c.writer.Write(payload[total:])
		if n < 0 || n > len(payload)-total {
			return total, io.ErrShortWrite
		}
		total += n
		if writeErr != nil {
			return total, writeErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}
