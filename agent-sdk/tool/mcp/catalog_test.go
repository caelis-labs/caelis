package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func catalogTool(server *mcpsdk.Server, name, description string, calls *atomic.Int32) {
	server.AddTool(&mcpsdk.Tool{Name: name, Description: description, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		if calls != nil {
			calls.Add(1)
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: description}}}, nil
	})
}
func catalogManager(t *testing.T, server *mcpsdk.Server, enabled bool) *Manager {
	t.Helper()
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	t.Cleanup(endpoint.Close)
	mgr, err := NewManagerWithOptions(t.Context(), []ServerSpec{{PluginID: "p", Name: "local", Transport: TransportStreamableHTTP, URL: endpoint.URL}}, nil, ManagerOptions{RefreshTools: enabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	select {
	case <-mgr.Initialized():
	case <-time.After(5 * time.Second):
		t.Fatal("MCP initialization timed out")
	}
	return mgr
}
func awaitCatalog(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("catalog condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func catalogClean(m *Manager) bool {
	c := m.servers[0].client.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.dirty
}

func TestCatalogHTTPRefreshRevokesPinnedSchemaAndRetainsInflightResult(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "local", Version: "1"}, &mcpsdk.ServerOptions{PageSize: 1})
	var calls atomic.Int32
	catalogTool(server, "read", "v1", &calls)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	server.AddTool(&mcpsdk.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		close(entered)
		<-release
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "original receipt"}}}, nil
	})
	mgr := catalogManager(t, server, true)
	old := mgr.Tools()[0]
	slow := mgr.Tools()[1]
	done := make(chan tool.Result, 1)
	go func() {
		result, _ := slow.Call(t.Context(), tool.Call{ID: "original", Name: "local__slow", Input: []byte(`{}`)})
		done <- result
	}()
	<-entered
	catalogTool(server, "read", "v2", &calls)
	server.RemoveTools("slow")
	awaitCatalog(t, func() bool {
		return len(mgr.Tools()) == 1 && strings.HasSuffix(mgr.Tools()[0].Definition().Description, " v2") && catalogClean(mgr)
	})
	if _, err := old.Call(t.Context(), tool.Call{Input: []byte(`{}`)}); err == nil {
		t.Fatal("old schema was dispatched")
	}
	if calls.Load() != 0 {
		t.Fatal("stale call reached server")
	}
	releaseOnce.Do(func() { close(release) })
	if result := <-done; result.ID != "original" || result.IsError || result.Content[0].Text.Text != "original receipt" {
		t.Fatalf("lost inflight receipt: %+v", result)
	}
	fresh := mgr.Tools()[0]
	if tool.BindingForDefinition(old.Definition()).Identity != tool.BindingForDefinition(fresh.Definition()).Identity || tool.BindingForDefinition(old.Definition()).Version == tool.BindingForDefinition(fresh.Definition()).Version {
		t.Fatal("identity/schema version binding incorrect")
	}
	if _, err := fresh.Call(t.Context(), tool.Call{Input: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	server.RemoveTools("read")
	awaitCatalog(t, func() bool { return len(mgr.Tools()) == 0 && catalogClean(mgr) })
	catalogTool(server, "read", "v2", &calls)
	awaitCatalog(t, func() bool { return len(mgr.Tools()) == 1 && catalogClean(mgr) })
	if _, err := fresh.Call(t.Context(), tool.Call{}); err == nil {
		t.Fatal("remove/re-add revived pending old handle")
	}
	fresh = mgr.Tools()[0]
	if err := mgr.RevokeServer("p", "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Call(t.Context(), tool.Call{Input: []byte(`{}`)}); err == nil || len(mgr.Tools()) != 0 || calls.Load() != 1 {
		t.Fatal("revocation did not gate pinned callable")
	}
}

func TestCatalogPaginationFailureCancellationAndNotificationCoalescing(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "local", Version: "1"}, &mcpsdk.ServerOptions{PageSize: 1})
	catalogTool(server, "a", "a", nil)
	catalogTool(server, "b", "b", nil)
	var mode atomic.Int32
	var lists atomic.Int32
	blocked, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/list" {
				lists.Add(1)
				switch mode.Load() {
				case 1:
					return nil, errors.New("temporary listing failure")
				case 2:
					return &mcpsdk.ListToolsResult{Tools: []*mcpsdk.Tool{{Name: "a", InputSchema: map[string]any{"type": "object"}}}, NextCursor: "cycle"}, nil
				case 3:
					select {
					case blocked <- struct{}{}:
					default:
					}
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			}
			return next(ctx, method, req)
		}
	})
	mgr := catalogManager(t, server, true)
	before := []tool.Definition{mgr.Tools()[0].Definition(), mgr.Tools()[1].Definition()}
	for _, failure := range []int32{1, 2} {
		mode.Store(failure)
		if err := mgr.RefreshTools(t.Context(), "p", "local"); err == nil {
			t.Fatal("failed list committed")
		}
		after := mgr.Tools()
		if len(after) != 2 || !reflect.DeepEqual(before, []tool.Definition{after[0].Definition(), after[1].Definition()}) {
			t.Fatal("failure replaced whole directory")
		}
		if _, err := after[0].Call(t.Context(), tool.Call{}); err == nil {
			t.Fatal("uncertain catalog allowed call")
		}
	}
	mode.Store(3)
	catalogTool(server, "c", "c", nil)
	<-blocked
	baseline := lists.Load()
	c := mgr.servers[0].client.catalog
	c.mu.Lock()
	generation := c.generation
	c.mu.Unlock()
	for i := range 20 {
		catalogTool(server, "c", string(rune('a'+i)), nil)
	}
	// Wait for the actual HTTP notification receiver, not an in-memory hook.
	awaitCatalog(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.generation > generation })
	mode.Store(0)
	releaseOnce.Do(func() { close(release) })
	awaitCatalog(t, func() bool { return len(mgr.Tools()) == 3 && catalogClean(mgr) })
	if got := lists.Load() - baseline; got > 12 {
		t.Fatalf("notifications were not coalesced: %d page reads", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := mgr.RefreshTools(ctx, "p", "local"); err == nil {
		t.Fatal("cancelled refresh succeeded")
	}
	// Original connection remains usable after a safe retry, without restart.
	if err := mgr.RefreshTools(t.Context(), "p", "local"); err != nil {
		t.Fatal(err)
	}
	if len(mgr.Tools()) != 3 {
		t.Fatal("recovery lost catalog")
	}
}

func TestCatalogRefreshDefaultOffAndReconnectDoesNotReviveOldHandles(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "local", Version: "1"}, nil)
	catalogTool(server, "a", "a", nil)
	disabled := catalogManager(t, server, false)
	if disabled.servers[0].client.catalog != nil {
		t.Fatal("refresh enabled by default")
	}
	if err := disabled.RefreshTools(t.Context(), "p", "local"); err == nil {
		t.Fatal("disabled refresh accepted")
	}
	enabled := catalogManager(t, server, true)
	old := enabled.Tools()[0]
	if err := enabled.Close(); err != nil {
		t.Fatal(err)
	}
	reconnected := catalogManager(t, server, true)
	if _, err := old.Call(t.Context(), tool.Call{}); err == nil {
		t.Fatal("reconnect revived closed handle")
	}
	if result, err := reconnected.Tools()[0].Call(t.Context(), tool.Call{}); err != nil || result.IsError {
		t.Fatalf("new connection unavailable: %v %+v", err, result)
	}
}

func TestCatalogPaginationRacePublishesNoMixedSnapshot(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "pages", Version: "1"}, &mcpsdk.ServerOptions{PageSize: 1})
	catalogTool(server, "a", "a", nil)
	catalogTool(server, "b", "b", nil)
	var block atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/list" && block.Load() {
				if params, ok := req.GetParams().(*mcpsdk.ListToolsParams); ok && params != nil && params.Cursor != "" {
					select {
					case entered <- struct{}{}:
					default:
					}
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			}
			return next(ctx, method, req)
		}
	})
	mgr := catalogManager(t, server, true)
	revision := mgr.CatalogRevision()
	block.Store(true)
	done := make(chan error, 1)
	go func() { done <- mgr.RefreshTools(t.Context(), "p", "local") }()
	<-entered
	if ready := mgr.Tools(); len(ready) != 2 || ready[1].Definition().Name != "local__b" {
		t.Fatal("partial page was published")
	}
	server.RemoveTools("b")
	catalogTool(server, "c", "c", nil)
	c := mgr.servers[0].client.catalog
	awaitCatalog(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.generation >= 2 })
	if revision == mgr.CatalogRevision() {
		t.Fatal("candidate revision survived notification")
	}
	block.Store(false)
	releaseOnce.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	awaitCatalog(t, func() bool {
		return len(mgr.Tools()) == 2 && mgr.Tools()[1].Definition().Name == "local__c" && catalogClean(mgr)
	})
}

func TestCatalogCollisionOwnerDoesNotTransferAfterRemoval(t *testing.T) {
	first := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "first", Version: "1"}, nil)
	second := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "second", Version: "1"}, nil)
	catalogTool(first, "read", "first", nil)
	catalogTool(second, "read", "second", nil)
	var specs []ServerSpec
	for i, server := range []*mcpsdk.Server{first, second} {
		endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
		defer endpoint.Close()
		specs = append(specs, ServerSpec{PluginID: fmt.Sprintf("p%d", i), Name: "shared", Transport: TransportStreamableHTTP, URL: endpoint.URL})
	}
	mgr, err := NewManagerWithOptions(t.Context(), specs, nil, ManagerOptions{RefreshTools: true})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	<-mgr.Initialized()
	if ready := mgr.Tools(); len(ready) != 1 || !strings.HasSuffix(ready[0].Definition().Description, " first") {
		t.Fatalf("wrong collision winner: %v", ready)
	}
	first.RemoveTools("read")
	awaitCatalog(t, func() bool { return len(mgr.Tools()) == 0 })
	if err := mgr.RefreshTools(t.Context(), "p1", "shared"); err != nil {
		t.Fatal(err)
	}
	if len(mgr.Tools()) != 0 {
		t.Fatal("removed owner silently promoted same-name tool")
	}
}

func TestCatalogModernHTTPDoesNotReuseIndependentPageCache(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "modern", Version: "1"}, nil)
	catalogTool(server, "a", "a", nil)
	var lists atomic.Int32
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			result, err := next(ctx, method, req)
			if method == "tools/list" {
				lists.Add(1)
				if page, ok := result.(*mcpsdk.ListToolsResult); ok {
					page.TTLMs = 60000
				}
			}
			return result, err
		}
	})
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{Stateless: true}))
	defer endpoint.Close()
	mgr, err := NewManagerWithOptions(t.Context(), []ServerSpec{{PluginID: "p", Name: "modern", URL: endpoint.URL, Transport: TransportStreamableHTTP}}, nil, ManagerOptions{RefreshTools: true})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	<-mgr.Initialized()
	if len(mgr.Tools()) != 1 {
		t.Fatalf("initial modern directory=%+v", mgr.GetServerInfos("p"))
	}
	if got := mgr.servers[0].client.session.InitializeResult().ProtocolVersion; got != mcpProtocolVersion20260728 {
		t.Fatalf("modern test negotiated %q", got)
	}
	before := lists.Load()
	if err := mgr.RefreshTools(t.Context(), "p", "modern"); err != nil {
		t.Fatal(err)
	}
	if lists.Load() <= before {
		t.Fatal("explicit refresh reused SDK page cache")
	}
	catalogTool(server, "b", "b", nil)
	awaitCatalog(t, func() bool { return len(mgr.Tools()) == 2 && catalogClean(mgr) })
}

func TestCatalogCloseCancelsRefreshAndDrainsWorker(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "closing", Version: "1"}, nil)
	catalogTool(server, "a", "a", nil)
	var block atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/list" && block.Load() {
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-ctx.Done():
				case <-release:
				}
				return nil, ctx.Err()
			}
			return next(ctx, method, req)
		}
	})
	mgr := catalogManager(t, server, true)
	block.Store(true)
	catalogTool(server, "b", "b", nil)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	done := make(chan error, 1)
	go func() { done <- mgr.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		// The SDK may spend five seconds on best-effort session DELETE after
		// local cancellation. A remote handler is not required to stop first.
		t.Fatal("close did not drain refresh within SDK cleanup bound")
	}
	if len(mgr.Tools()) != 0 {
		t.Fatal("closed manager retained tools")
	}
}
