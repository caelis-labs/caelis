package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"

	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
)

const adapterVersion = "0.1.0"

// Backend is one shared Codex app-server JSON-RPC generation. It multiplexes
// independent ACP connections and enforces a single live owner per Thread.
type Backend struct {
	rpc *appserver.Connection

	mu     sync.Mutex
	routes map[string]*sessionRoute
	closed bool
}

// NewBackend binds an already-started Codex app-server stream and performs its
// initialization handshake. Process start, stderr, termination, and Wait stay
// with the embedding Host.
func NewBackend(ctx context.Context, input io.Reader, output io.Writer) (*Backend, error) {
	b := &Backend{routes: make(map[string]*sessionRoute)}
	rpc, err := appserver.NewConnection(input, output, b.onNotification, b.onRequest)
	if err != nil {
		return nil, err
	}
	b.rpc = rpc
	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var response struct {
		CodexHome string `json:"codexHome"`
	}
	err = rpc.Request(initCtx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name": "caelis-codex-acp", "title": "Caelis Codex ACP Adapter", "version": adapterVersion,
		},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}, &response)
	if err != nil {
		_ = rpc.Close()
		return nil, fmt.Errorf("codex adapter: initialize app-server: %w", err)
	}
	return b, nil
}

// Done closes when this backend generation is lost.
func (b *Backend) Done() <-chan struct{} { return b.rpc.Done() }

// Err reports the terminal backend failure.
func (b *Backend) Err() error { return b.rpc.Err() }

// Close rejects future channels and closes all existing ACP connections. It
// does not terminate or wait for the Host-owned process.
func (b *Backend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	routes := make([]*sessionRoute, 0, len(b.routes))
	for _, route := range b.routes {
		routes = append(routes, route)
	}
	b.routes = make(map[string]*sessionRoute)
	b.mu.Unlock()
	for _, route := range routes {
		route.close(errors.New("codex adapter: backend closed"))
	}
	return b.rpc.Close()
}

// ServeACP serves one standard ACP connection on caller-supplied streams.
func (b *Backend) ServeACP(ctx context.Context, options ConnectionOptions, input io.Reader, output io.Writer) error {
	if input == nil || output == nil {
		return errors.New("codex adapter: ACP input and output are required")
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return errors.New("codex adapter: backend is closed")
	}
	a := &agent{
		backend: b, options: normalizeConnectionOptions(options),
		sessions: make(map[string]*sessionState), connectionReady: make(chan struct{}),
	}
	connection, err := acp.NewAgentSideConnectionWithOptions(a, output, input, acp.ConnectionOptions{})
	if err != nil {
		return err
	}
	a.connection = connection
	close(a.connectionReady)
	defer func() {
		a.closeAll()
		_ = connection.Close()
	}()
	wait := make(chan error, 1)
	go func() { wait <- connection.Wait(context.Background()) }()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-b.Done():
		return fmt.Errorf("codex adapter: backend lost: %w", b.Err())
	case err := <-wait:
		if errors.Is(err, acp.ErrPeerClosed) {
			return nil
		}
		return err
	}
}

func normalizeConnectionOptions(in ConnectionOptions) ConnectionOptions {
	in.ConnectionID = strings.TrimSpace(in.ConnectionID)
	if in.ConnectionID == "" {
		in.ConnectionID = "codex-acp"
	}
	in.Workspace.AllowedRoots = append([]string(nil), in.Workspace.AllowedRoots...)
	in.Workspace.WritableRoots = append([]string(nil), in.Workspace.WritableRoots...)
	return in
}

func (b *Backend) acquire(threadID string, route *sessionRoute) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || route == nil {
		return errors.New("codex adapter: thread route is incomplete")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("codex adapter: backend is closed")
	}
	if existing := b.routes[threadID]; existing != nil && existing != route {
		return fmt.Errorf("codex adapter: session %q is busy on another ACP connection", threadID)
	}
	b.routes[threadID] = route
	return nil
}

func (b *Backend) release(threadID string, route *sessionRoute) {
	b.mu.Lock()
	if b.routes[strings.TrimSpace(threadID)] == route {
		delete(b.routes, strings.TrimSpace(threadID))
	}
	b.mu.Unlock()
}

func (b *Backend) route(threadID string) *sessionRoute {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.routes[strings.TrimSpace(threadID)]
}

func (b *Backend) onNotification(notification appserver.Notification) {
	threadID := notificationThreadID(notification.Params)
	if threadID == "" {
		return
	}
	if route := b.route(threadID); route != nil {
		route.enqueue(notification)
	}
}

func (b *Backend) onRequest(ctx context.Context, request appserver.Request) (any, error) {
	threadID := notificationThreadID(request.Params)
	if route := b.route(threadID); route != nil {
		return route.handleRequest(ctx, request)
	}
	return appServerFallbackResponse(request.Method)
}

func notificationThreadID(params json.RawMessage) string {
	var envelope struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(params, &envelope) != nil {
		return ""
	}
	if strings.TrimSpace(envelope.ThreadID) != "" {
		return strings.TrimSpace(envelope.ThreadID)
	}
	return strings.TrimSpace(envelope.Thread.ID)
}

func appServerFallbackResponse(method string) (any, error) {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]any{"decision": "cancel"}, nil
	case "item/permissions/requestApproval":
		return map[string]any{"permissions": map[string]any{}, "scope": "turn", "strictAutoReview": false}, nil
	case "item/tool/requestUserInput":
		return map[string]any{"answers": map[string]any{}}, nil
	case "mcpServer/elicitation/request":
		return map[string]any{"action": "cancel", "content": nil, "_meta": nil}, nil
	default:
		return nil, &appserver.Error{Code: -32601, Message: "method not found: " + method}
	}
}
