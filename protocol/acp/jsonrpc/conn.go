package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

const JSONRPCVersion = "2.0"

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// CallError preserves the structured JSON-RPC error returned by a peer.
// Callers that need protocol-specific recovery must inspect Code instead of
// parsing the formatted error string.
type CallError struct {
	Code    int
	Message string
	Data    any
}

func (e *CallError) Error() string {
	if e == nil {
		return ""
	}
	return formatRPCError(e.Code, e.Message, e.Data)
}

// ErrorCode returns the peer-supplied JSON-RPC error code when err originated
// from a completed RPC call.
func ErrorCode(err error) (int, bool) {
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr == nil {
		return 0, false
	}
	return callErr.Code, true
}

type RequestHandler func(context.Context, Message) (any, *RPCError)
type NotificationHandler func(context.Context, Message)

type Conn struct {
	reader io.Reader
	writer io.Writer

	writes      *writerArbiter
	pending     sync.Map
	nextID      atomic.Int64
	lifecycleMu sync.RWMutex
	lifecycle   context.Context
}

type pendingCall struct {
	ch chan pendingCallResult

	mu               sync.Mutex
	resolved         bool
	responseObserver func(Message) error
}

type pendingCallResult struct {
	message Message
	err     error
}

type PostWriteResult struct {
	Payload    any
	AfterWrite func()
}

func New(reader io.Reader, writer io.Writer) *Conn {
	return &Conn{reader: reader, writer: writer, writes: newWriterArbiter()}
}

func (c *Conn) Serve(ctx context.Context, onRequest RequestHandler, onNotification NotificationHandler) error {
	if c == nil {
		return fmt.Errorf("acp/jsonrpc: conn is nil")
	}
	c.lifecycleMu.Lock()
	c.lifecycle = ctx
	c.lifecycleMu.Unlock()
	var serveErr error
	defer func() {
		c.failPending(serveErr)
	}()
	done := make(chan struct{})
	defer close(done)
	if closer, ok := c.reader.(io.Closer); ok {
		go func() {
			select {
			case <-ctx.Done():
				_ = closer.Close()
			case <-done:
			}
		}()
	}
	reader := bufio.NewReader(c.reader)
	for {
		select {
		case <-ctx.Done():
			serveErr = ctx.Err()
			return serveErr
		default:
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if ctx.Err() != nil {
				serveErr = ctx.Err()
				return serveErr
			}
			if errors.Is(err, io.EOF) {
				serveErr = io.EOF
				return nil
			}
			serveErr = err
			return serveErr
		}
		if len(line) == 0 {
			continue
		}
		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			_ = c.writeMessageContext(ctx, Message{
				JSONRPC: JSONRPCVersion,
				Error:   &RPCError{Code: -32700, Message: "parse error"},
			})
			continue
		}
		if msg.JSONRPC == "" {
			msg.JSONRPC = JSONRPCVersion
		}
		if msg.Method == "" {
			if msg.ID != nil {
				c.resolvePending(msg)
			}
			continue
		}
		if msg.ID == nil {
			if onNotification != nil {
				onNotification(ctx, msg)
			}
			continue
		}
		go func(req Message) {
			if onRequest == nil {
				_ = c.writeMessageContext(ctx, Message{
					JSONRPC: JSONRPCVersion,
					ID:      req.ID,
					Error:   &RPCError{Code: -32601, Message: "method not found"},
				})
				return
			}
			result, rpcErr := onRequest(ctx, req)
			var afterWrite func()
			switch wrapped := result.(type) {
			case PostWriteResult:
				result = wrapped.Payload
				afterWrite = wrapped.AfterWrite
			case *PostWriteResult:
				if wrapped != nil {
					result = wrapped.Payload
					afterWrite = wrapped.AfterWrite
				}
			}
			resp := Message{JSONRPC: JSONRPCVersion, ID: req.ID}
			switch {
			case rpcErr != nil:
				resp.Error = rpcErr
			case result == nil:
				resp.Result = map[string]any{}
			default:
				resp.Result = result
			}
			if err := c.writeMessageContext(ctx, resp); err != nil {
				return
			}
			if afterWrite != nil {
				afterWrite()
			}
		}(msg)
	}
}

func (c *Conn) Notify(method string, params any) error {
	return c.NotifyContext(c.lifecycleContext(), method, params)
}

// NotifyContext writes one notification through the same cancellable FIFO
// writer admission used by calls and request responses.
func (c *Conn) NotifyContext(ctx context.Context, method string, params any) error {
	return c.writeMessageContext(ctx, Message{
		JSONRPC: JSONRPCVersion,
		Method:  method,
		Params:  MustMarshalRaw(params),
	})
}

func (c *Conn) lifecycleContext() context.Context {
	if c == nil {
		return context.Background()
	}
	c.lifecycleMu.RLock()
	ctx := c.lifecycle
	c.lifecycleMu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (c *Conn) Call(ctx context.Context, method string, params any, out any) error {
	return c.call(ctx, method, params, out, nil)
}

// CallWithAbort performs one call and invokes abort only when cancellation
// races a transport write that has already started. The transport owner must
// revoke that exact writer so the call can return with an ambiguous outcome.
func (c *Conn) CallWithAbort(ctx context.Context, method string, params any, out any, abort func()) error {
	return c.call(ctx, method, params, out, abort)
}

func (c *Conn) call(ctx context.Context, method string, params any, out any, abort func()) error {
	call, err := c.PrepareCall(method, params)
	if err != nil {
		return err
	}
	if abort == nil {
		err = call.Dispatch(ctx)
	} else {
		err = call.DispatchWithAbort(ctx, abort)
	}
	if err != nil {
		return err
	}
	err = call.Wait(ctx, out)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Dispatch completed the full request frame before Wait took ownership.
		// Caller cancellation can no longer prove that the peer saw no effect.
		return &DispatchError{Cause: err, WriteStarted: true, AnyWritten: true}
	}
	return err
}

func FormatRPCError(rpcErr *RPCError) error {
	if rpcErr == nil {
		return nil
	}
	return &CallError{
		Code:    rpcErr.Code,
		Message: rpcErr.Message,
		Data:    rpcErr.Data,
	}
}

func formatRPCError(code int, message string, data any) string {
	msg := fmt.Sprintf("acp rpc error %d: %s", code, message)
	if formatted := formatRPCErrorData(data); formatted != "" && formatted != "null" {
		msg += " (data: " + formatted + ")"
	}
	return msg
}

func formatRPCErrorData(data any) string {
	if data == nil {
		return ""
	}
	if text, ok := data.(string); ok {
		return text
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprint(data)
	}
	return string(raw)
}

func (c *Conn) resolvePending(msg Message) {
	deliver := func(value any) {
		call, ok := value.(*pendingCall)
		if !ok || call == nil {
			return
		}
		call.mu.Lock()
		call.resolved = true
		observe := call.responseObserver
		call.mu.Unlock()
		if observe != nil {
			if err := observe(msg); err != nil {
				select {
				case call.ch <- pendingCallResult{err: err}:
				default:
				}
				return
			}
		}
		select {
		case call.ch <- pendingCallResult{message: msg}:
		default:
		}
	}
	switch id := msg.ID.(type) {
	case float64:
		if pending, ok := c.pending.Load(int64(id)); ok {
			deliver(pending)
		}
	case int64:
		if pending, ok := c.pending.Load(id); ok {
			deliver(pending)
		}
	case int:
		if pending, ok := c.pending.Load(int64(id)); ok {
			deliver(pending)
		}
	case json.Number:
		if n, err := id.Int64(); err == nil {
			if pending, ok := c.pending.Load(n); ok {
				deliver(pending)
			}
		}
	}
}

func (c *Conn) failPending(cause error) {
	if c == nil {
		return
	}
	callErr := pendingCallError(cause)
	c.pending.Range(func(key, value any) bool {
		call, ok := value.(*pendingCall)
		if !ok {
			c.pending.Delete(key)
			return true
		}
		select {
		case call.ch <- pendingCallResult{err: callErr}:
		default:
		}
		c.pending.Delete(key)
		return true
	})
}

func pendingCallError(cause error) error {
	if cause == nil {
		cause = io.EOF
	}
	return fmt.Errorf("acp/jsonrpc: connection closed before response: %w", cause)
}

func (c *Conn) writeMessageContext(ctx context.Context, msg Message) error {
	if msg.JSONRPC == "" {
		msg.JSONRPC = JSONRPCVersion
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.writeEncoded(ctx, append(data, '\n'), nil)
	return err
}

func MustMarshalRaw(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage([]byte("null"))
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage([]byte("null"))
	}
	return raw
}
