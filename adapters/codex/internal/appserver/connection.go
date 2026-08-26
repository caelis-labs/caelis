// Package appserver implements the Codex app-server JSON-RPC transport.
package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

const maxMessageBytes = 64 << 20

// Error is one app-server JSON-RPC error.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("codex app-server error %d: %s", e.Code, e.Message)
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Notification is one server notification in reader arrival order.
type Notification struct {
	Sequence uint64
	Method   string
	Params   json.RawMessage
}

// Request is one server-to-client request.
type Request struct {
	Method string
	Params json.RawMessage
}

// RequestHandler resolves app-server requests such as approvals.
type RequestHandler func(context.Context, Request) (any, error)

// NotificationHandler receives app-server notifications. It must not block.
type NotificationHandler func(Notification)

type response struct {
	result json.RawMessage
	err    error
}

// Connection multiplexes one Codex app-server byte stream. It does not own or
// wait for the underlying OS process.
type Connection struct {
	input  io.Reader
	output io.Writer

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan response
	done    chan struct{}
	err     error

	nextID   atomic.Uint64
	sequence atomic.Uint64

	onNotification NotificationHandler
	onRequest      RequestHandler
	requestSlots   chan struct{}
}

// NewConnection starts the single app-server reader loop.
func NewConnection(input io.Reader, output io.Writer, notifications NotificationHandler, requests RequestHandler) (*Connection, error) {
	if input == nil || output == nil {
		return nil, errors.New("codex app-server: input and output are required")
	}
	c := &Connection{
		input: input, output: output,
		pending: make(map[string]chan response), done: make(chan struct{}),
		onNotification: notifications, onRequest: requests,
		requestSlots: make(chan struct{}, 32),
	}
	go c.readLoop()
	return c, nil
}

// Done closes when the backend byte stream is lost or Close is called.
func (c *Connection) Done() <-chan struct{} { return c.done }

// Err reports the terminal transport failure.
func (c *Connection) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Request sends one app-server request and waits for its correlated response.
func (c *Connection) Request(ctx context.Context, method string, params any, result any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	id := c.nextID.Add(1)
	key := strconv.FormatUint(id, 10)
	wait := make(chan response, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.pending[key] = wait
	c.mu.Unlock()

	if err := c.write(message{ID: json.RawMessage(key), Method: method, Params: marshalRaw(params)}); err != nil {
		c.removePending(key)
		return err
	}
	select {
	case <-ctx.Done():
		c.removePending(key)
		return context.Cause(ctx)
	case reply := <-wait:
		if reply.err != nil {
			return reply.err
		}
		if result == nil || len(reply.result) == 0 || bytes.Equal(bytes.TrimSpace(reply.result), []byte("null")) {
			return nil
		}
		if err := json.Unmarshal(reply.result, result); err != nil {
			return fmt.Errorf("codex app-server: decode %s response: %w", method, err)
		}
		return nil
	}
}

// Notify sends one app-server notification.
func (c *Connection) Notify(method string, params any) error {
	return c.write(message{Method: method, Params: marshalRaw(params)})
}

// Close terminates the logical connection without claiming process ownership.
func (c *Connection) Close() error {
	c.fail(errors.New("codex app-server: connection closed"))
	return nil
}

func (c *Connection) readLoop() {
	reader := bufio.NewReaderSize(c.input, 64<<10)
	for {
		line, err := readLine(reader, maxMessageBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			c.fail(fmt.Errorf("codex app-server: read: %w", err))
			return
		}
		var msg message
		if err := json.Unmarshal(line, &msg); err != nil {
			c.fail(fmt.Errorf("codex app-server: invalid JSON message: %w", err))
			return
		}
		switch {
		case len(msg.ID) > 0 && msg.Method != "":
			c.dispatchRequest(msg)
		case len(msg.ID) > 0:
			c.dispatchResponse(msg)
		case msg.Method != "":
			if c.onNotification != nil {
				c.onNotification(Notification{
					Sequence: c.sequence.Add(1), Method: msg.Method,
					Params: append(json.RawMessage(nil), msg.Params...),
				})
			}
		default:
			c.fail(errors.New("codex app-server: message has neither id nor method"))
			return
		}
	}
}

func (c *Connection) dispatchResponse(msg message) {
	key := string(bytes.TrimSpace(msg.ID))
	c.mu.Lock()
	wait := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if wait == nil {
		return
	}
	if msg.Error != nil {
		wait <- response{err: msg.Error}
		return
	}
	wait <- response{result: append(json.RawMessage(nil), msg.Result...)}
}

func (c *Connection) dispatchRequest(msg message) {
	select {
	case c.requestSlots <- struct{}{}:
		go func() {
			defer func() { <-c.requestSlots }()
			var result any
			var err error
			if c.onRequest == nil {
				err = &Error{Code: -32601, Message: "method not found"}
			} else {
				result, err = c.onRequest(context.Background(), Request{
					Method: msg.Method, Params: append(json.RawMessage(nil), msg.Params...),
				})
			}
			reply := message{ID: append(json.RawMessage(nil), msg.ID...)}
			if err != nil {
				var rpcErr *Error
				if !errors.As(err, &rpcErr) {
					rpcErr = &Error{Code: -32603, Message: err.Error()}
				}
				reply.Error = rpcErr
			} else {
				reply.Result = marshalRaw(result)
			}
			if writeErr := c.write(reply); writeErr != nil {
				c.fail(writeErr)
			}
		}()
	default:
		_ = c.write(message{ID: append(json.RawMessage(nil), msg.ID...), Error: &Error{Code: -32001, Message: "request queue full"}})
	}
}

func (c *Connection) write(msg message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("codex app-server: encode message: %w", err)
	}
	if len(encoded) > maxMessageBytes {
		return errors.New("codex app-server: message exceeds 64 MiB")
	}
	encoded = append(encoded, '\n')
	if _, err := c.output.Write(encoded); err != nil {
		return fmt.Errorf("codex app-server: write: %w", err)
	}
	return nil
}

func (c *Connection) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

func (c *Connection) fail(err error) {
	if err == nil {
		err = errors.New("codex app-server: connection stopped")
	}
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	pending := c.pending
	c.pending = make(map[string]chan response)
	close(c.done)
	c.mu.Unlock()
	for _, wait := range pending {
		wait <- response{err: err}
	}
}

func marshalRaw(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage("{}")
	}
	if raw, ok := value.(json.RawMessage); ok {
		return append(json.RawMessage(nil), raw...)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}

func readLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var frame []byte
	for {
		part, more, err := reader.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(frame)+len(part) > limit {
			return nil, fmt.Errorf("message exceeds %d bytes", limit)
		}
		frame = append(frame, part...)
		if !more {
			if len(bytes.TrimSpace(frame)) == 0 {
				frame = frame[:0]
				continue
			}
			return frame, nil
		}
	}
}
