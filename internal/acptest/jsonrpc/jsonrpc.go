// Package jsonrpc provides a small SDK-backed fake peer for repository tests.
// It intentionally owns no framing, pending-call, ordering, or writer logic.
package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

const maxFrameSize = 64 * 1024 * 1024

type Message struct {
	Method string
	Params json.RawMessage
}

type RPCError = acpsdk.RequestError

type RequestHandler func(context.Context, Message) (any, *RPCError)
type NotificationHandler func(context.Context, Message)

type Conn struct {
	reader io.Reader
	writer io.Writer

	mu    sync.RWMutex
	conn  *acpsdk.Connection
	ctx   context.Context
	ready chan struct{}
}

func New(reader io.Reader, writer io.Writer) *Conn {
	return &Conn{reader: reader, writer: writer, ready: make(chan struct{})}
}

func (c *Conn) Serve(ctx context.Context, onRequest RequestHandler, onNotification NotificationHandler) error {
	if c == nil || c.reader == nil || c.writer == nil {
		return errors.New("ACP test peer streams are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ready := make(chan struct{})
	connection, err := acpsdk.NewConnectionWithOptions(
		func(handlerCtx context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
			<-ready
			message := Message{Method: method, Params: append(json.RawMessage(nil), params...)}
			if onRequest != nil {
				return onRequest(handlerCtx, message)
			}
			if onNotification != nil {
				onNotification(handlerCtx, message)
			}
			return nil, nil
		},
		c.writer,
		c.reader,
		acpsdk.ConnectionOptions{MaxFrameSize: maxFrameSize},
	)
	if err != nil {
		close(ready)
		return err
	}
	c.mu.Lock()
	c.conn = connection
	c.ctx = ctx
	c.mu.Unlock()
	close(ready)
	close(c.ready)

	waitDone := make(chan error, 1)
	go func() { waitDone <- connection.Wait(context.Background()) }()
	select {
	case <-ctx.Done():
		_ = connection.Close()
		<-waitDone
		return context.Cause(ctx)
	case err := <-waitDone:
		if errors.Is(err, acpsdk.ErrPeerClosed) || errors.Is(err, acpsdk.ErrConnectionClosed) {
			return nil
		}
		return err
	}
}

func (c *Conn) Notify(method string, params any) error {
	connection, ctx, err := c.connection(context.Background())
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return connection.SendNotification(ctx, method, params)
}

func (c *Conn) Call(ctx context.Context, method string, params any, out any) error {
	connection, _, err := c.connection(ctx)
	if err != nil {
		return err
	}
	result, err := acpsdk.SendRequest[json.RawMessage](connection, ctx, method, params)
	if err != nil || out == nil || len(result) == 0 {
		return err
	}
	return json.Unmarshal(result, out)
}

func (c *Conn) connection(ctx context.Context) (*acpsdk.Connection, context.Context, error) {
	if c == nil {
		return nil, nil, errors.New("ACP test peer is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, nil, context.Cause(ctx)
	case <-c.ready:
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil {
		return nil, nil, errors.New("ACP test peer is not serving")
	}
	return c.conn, c.ctx, nil
}

func (c *Conn) Close() error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	connection := c.conn
	c.mu.RUnlock()
	if connection == nil {
		return nil
	}
	return connection.Close()
}

func MustMarshalRaw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
