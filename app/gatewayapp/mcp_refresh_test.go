package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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
			var fail atomic.Bool
			var lists atomic.Int32
			server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
				return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
					if method == "tools/list" {
						lists.Add(1)
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
			var ready func() bool
			if profile {
				ready = func() bool {
					status := active.instance.capabilityStatus(1)
					return len(status.Servers) == 1 && status.Servers[0].Status == "running" && status.Servers[0].Warning == ""
				}
			} else {
				<-active.instance.mcpMgr.Initialized()
				ready = func() bool {
					infos := active.instance.mcpMgr.GetServerInfos(pluginID)
					return len(infos) == 1 && infos[0].Status == "running" && infos[0].Warning == ""
				}
			}
			awaitExperimentalRefresh(t, ready)
			fail.Store(true)
			before := lists.Load()
			install("second")
			awaitExperimentalRefresh(t, func() bool { return lists.Load() > before && !ready() })
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
			awaitExperimentalRefresh(t, ready)
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
			// A new activation with the switch off must reject the same remote action.
			doc, err = stack.composition.authorities.store.LoadContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			doc.Experimental[experimental.MCPRefresh] = false
			if err = stack.composition.authorities.store.Save(doc); err != nil {
				t.Fatal(err)
			}
			create.OperationID = "create-disabled"
			disabled, err := client.CreateSession(t.Context(), create)
			if err != nil {
				t.Fatal(err)
			}
			_, releaseDisabled := activateHeldSessionRuntime(t, stack, disabled.SessionID)
			defer releaseDisabled()
			request.WriteBase = appserver.WriteBase{OperationID: "disabled", SessionID: disabled.SessionID}
			if result, err := client.RefreshMCPTools(t.Context(), request); err == nil || result.Outcome != appserver.OutcomeRejected {
				t.Fatal("disabled refresh accepted")
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
