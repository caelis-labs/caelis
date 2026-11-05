package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/experimental"
	"github.com/caelis-labs/caelis/control/mcpconfig"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExperimentalHTTPMCPRefreshRetriesOwnedActiveManager(t *testing.T) {
	for _, profile := range []bool{false, true} {
		t.Run(fmt.Sprint(profile), func(t *testing.T) {
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "retry", Version: "1"}, nil)
			install := func(version string) {
				server.AddTool(&mcpsdk.Tool{Name: "read", Description: version, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
					return &mcpsdk.CallToolResult{}, nil
				})
			}
			install("first")
			var promotedStartup atomic.Bool
			initializing, initializeRelease := make(chan struct{}), make(chan struct{})
			var initializeOnce sync.Once
			var fail atomic.Bool
			var lists atomic.Int32
			initialListing, initialListRelease := make(chan struct{}), make(chan struct{})
			backgroundListing, backgroundRelease := make(chan struct{}), make(chan struct{})
			var holdBackground atomic.Bool
			var releaseInitialOnce, releaseBackgroundOnce sync.Once
			releaseInitial := func() { releaseInitialOnce.Do(func() { close(initialListRelease) }) }
			releaseBackground := func() { releaseBackgroundOnce.Do(func() { close(backgroundRelease) }) }
			defer releaseInitial()
			defer releaseBackground()
			server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
				return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
					if method == "initialize" && promotedStartup.Load() {
						initializeOnce.Do(func() { close(initializing) })
						select {
						case <-initializeRelease:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					if method == "tools/list" {
						count := lists.Add(1)
						if profile && count == 1 {
							close(initialListing)
							select {
							case <-initialListRelease:
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						}
						if holdBackground.CompareAndSwap(true, false) {
							close(backgroundListing)
							select {
							case <-backgroundRelease:
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						}
						if fail.Load() {
							return nil, errors.New("temporary listing failure")
						}
					}
					return next(ctx, method, req)
				}
			})
			endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			t.Cleanup(endpoint.Close)
			stack, client := newExperimentalHTTPStack(t)
			read, err := client.ExperimentalFeatures(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			revision, _ := strconv.ParseUint(read.Revision, 10, 64)
			if _, err = client.SetExperimentalFeatures(t.Context(), appserver.ExperimentalRequest{WriteBase: appserver.WriteBase{OperationID: "enable-refresh", ExpectedRevision: &revision}, Values: experimental.Values{experimental.MCPRefresh: true}}); err != nil {
				t.Fatal(err)
			}
			doc, err := stack.composition.authorities.store.LoadContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			doc.MCPServers = mcpconfig.Servers{"local": {Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}
			if err = stack.composition.authorities.store.Save(doc); err != nil {
				t.Fatal(err)
			}
			create := appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-retry"}}
			pluginID := mcpconfig.NamespaceUser
			if profile {
				pluginID = "application"
				create.Profile = &sessionprofile.Profile{Version: "retry/1", Model: "ollama/compact-test", ToolsVersion: "tools/1", Execution: "tools-only", MCPServers: []sessionprofile.MCPServer{{Name: "local", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}}
			}
			created, err := client.CreateSession(t.Context(), create)
			if err != nil {
				t.Fatal(err)
			}
			request := appserver.RefreshMCPToolsRequest{WriteBase: appserver.WriteBase{OperationID: "dormant", SessionID: created.SessionID}, PluginID: pluginID, ServerName: "local"}
			result, err := client.RefreshMCPTools(t.Context(), request)
			if err == nil || result.Outcome != appserver.OutcomeConflicted || lists.Load() != 0 {
				t.Fatalf("dormant retry activated directory: %+v %v", result, err)
			}
			active, release := activateHeldSessionRuntime(t, stack, created.SessionID)
			defer release()
			catalogStatus := func(active *sessionRuntime) sessionprofile.MCPServerStatus {
				if profile {
					for _, server := range active.instance.capabilityStatus(1).Servers {
						if server.Name == "local" {
							return server
						}
					}
				} else {
					for _, server := range active.instance.mcpMgr.GetServerInfos(pluginID) {
						if server.Name == "local" {
							return applicationMCPServerStatus(server)
						}
					}
				}
				return sessionprofile.MCPServerStatus{}
			}
			ready := func(active *sessionRuntime) bool {
				server := catalogStatus(active)
				return server.Status == "running" && server.Warning == "" && !server.Stale && !server.Refreshing && !server.RefreshFailed && len(server.Tools) == 1 && server.Tools[0] == "read"
			}
			var manualRefreshes atomic.Int32
			if profile {
				active.instance.mu.Lock()
				refresh := active.instance.capabilityRefresh
				active.instance.capabilityRefresh = func(ctx context.Context, pluginID, name string) error {
					manualRefreshes.Add(1)
					return refresh(ctx, pluginID, name)
				}
				active.instance.mu.Unlock()
				select {
				case <-initialListing:
				case <-time.After(5 * time.Second):
					t.Fatal("initial catalog read did not start")
				}
				// A connected transport is visible before its first directory read.
				// Holding that read makes the fixture's readiness contract deterministic.
				if ready(active) {
					releaseInitial()
					t.Fatal("fixture treated a connected but unlisted catalog as ready")
				}
				releaseInitial()
			}
			awaitExperimentalRefresh(t, func() bool { return ready(active) })
			fail.Store(true)
			before := lists.Load()
			install("second")
			awaitExperimentalRefresh(t, func() bool {
				server := catalogStatus(active)
				return lists.Load() > before && server.Stale && server.RefreshFailed && !server.Refreshing
			})
			// No new notification is emitted when the remote server becomes healthy.
			fail.Store(false)
			current, err := stack.composition.sessions.Session(t.Context(), session.SessionRef{SessionID: created.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			request.WriteBase = appserver.WriteBase{OperationID: "recover-list", SessionID: created.SessionID, ExpectedRevision: &current.Revision, ExpectedControllerEpoch: current.Controller.EpochID}
			result, err = client.RefreshMCPTools(t.Context(), request)
			if err != nil || result.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("retry=%+v %v", result, err)
			}
			awaitExperimentalRefresh(t, func() bool { return ready(active) })
			if profile && manualRefreshes.Load() != 1 {
				t.Fatalf("manual refresh positive control: got %d calls", manualRefreshes.Load())
			}
			recoveredRequest := request
			count := lists.Load()
			replay, err := client.RefreshMCPTools(t.Context(), request)
			if err != nil || replay.OperationID != result.OperationID || lists.Load() != count {
				t.Fatalf("receipt re-executed tools/list: %+v %v", replay, err)
			}
			request.OperationID = "unauthorized"
			denied, err := stack.ExperimentalFeatures().Commands.RefreshMCPTools(t.Context(), appserver.Principal{ID: "not-the-owner"}, request)
			if err == nil || denied.Outcome != appserver.OutcomeRejected || lists.Load() != count {
				t.Fatal("foreign principal refreshed catalog")
			}
			request.OperationID = "stale"
			bad := uint64(999999)
			request.ExpectedRevision = &bad
			if result, err := client.RefreshMCPTools(t.Context(), request); err == nil || result.Outcome != appserver.OutcomeConflicted || lists.Load() != count {
				t.Fatal("stale session precondition reached MCP")
			}
			if profile {
				// Background notifications are independent of rejected manual commands.
				// Keep one real watcher request in flight and observe the command path too.
				holdBackground.Store(true)
				install("third")
				select {
				case <-backgroundListing:
				case <-time.After(5 * time.Second):
					t.Fatal("background catalog read did not start")
				}
				backgroundCount := lists.Load()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				if replay, err := client.RefreshMCPTools(ctx, recoveredRequest); err != nil || replay.OperationID != result.OperationID || replay.Outcome != appserver.OutcomeCommitted {
					t.Fatalf("receipt during background refresh: %+v %v", replay, err)
				}
				foreign := recoveredRequest
				foreign.OperationID = "foreign-during-background"
				if denied, err := stack.ExperimentalFeatures().Commands.RefreshMCPTools(ctx, appserver.Principal{ID: "not-the-owner"}, foreign); err == nil || denied.Outcome != appserver.OutcomeRejected {
					t.Fatalf("foreign refresh during background: %+v %v", denied, err)
				}
				request.OperationID = "stale-during-background"
				if denied, err := client.RefreshMCPTools(ctx, request); err == nil || denied.Outcome != appserver.OutcomeConflicted {
					t.Fatalf("stale refresh during background: %+v %v", denied, err)
				}
				if manualRefreshes.Load() != 1 || lists.Load() != backgroundCount {
					t.Fatalf("replay or rejected refresh reached MCP: manual=%d remote=%d want remote=%d", manualRefreshes.Load(), lists.Load(), backgroundCount)
				}
				releaseBackground()
				awaitExperimentalRefresh(t, func() bool { return ready(active) })
			}
			// Live refresh is built in: a fresh activation still serves the retry
			// even though the retired experimental key is absent.
			doc, err = stack.composition.authorities.store.LoadContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			delete(doc.Experimental, experimental.MCPRefresh)
			if err = stack.composition.authorities.store.Save(doc); err != nil {
				t.Fatal(err)
			}
			create.OperationID = "create-always-on"
			promoted, err := client.CreateSession(t.Context(), create)
			if err != nil {
				t.Fatal(err)
			}
			promotedStartup.Store(true)
			promotedActive, releasePromoted := activateHeldSessionRuntime(t, stack, promoted.SessionID)
			defer releasePromoted()
			if profile {
				_ = promotedActive.instance.capabilityStatus(1)
			}
			select {
			case <-initializing:
			case <-time.After(5 * time.Second):
				t.Fatal("promoted Manager did not start")
			}
			request.WriteBase = appserver.WriteBase{OperationID: "always-on", SessionID: promoted.SessionID}
			close(initializeRelease)
			// Activation starts MCP asynchronously. Refresh tests must wait on the
			// new activation's existing catalog status, not the previous Manager.
			awaitExperimentalRefresh(t, func() bool { return ready(promotedActive) })
			result, err = client.RefreshMCPTools(t.Context(), request)
			if err != nil || (result.Outcome != appserver.OutcomeCommitted && result.Outcome != appserver.OutcomeAccepted) {
				t.Fatalf("built-in refresh rejected: %+v %v", result, err)
			}
		})
	}
}

func awaitExperimentalRefresh(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("MCP state did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}
