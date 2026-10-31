package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPFixedRoutingRefreshDuringApprovalAndRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "lifecycle", Version: "1"}, nil)
	var effects atomic.Int32
	publish := func(kind string) {
		server.AddTool(&mcpsdk.Tool{Name: "read", Description: "Read a test value", InputSchema: map[string]any{"type": "object", "required": []any{"value"}, "properties": map[string]any{"value": map[string]any{"type": kind}}}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			effects.Add(1)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "read"}}}, nil
		})
	}
	publish("integer")
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	defer endpoint.Close()
	manager, err := mcp.NewManager(ctx, []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	<-manager.Initialized()
	if len(manager.Tools()) != 1 {
		t.Fatalf("catalog not ready: %+v", manager.GetServerInfos("fixture"))
	}
	root := t.TempDir()
	service := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "fixture", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	model := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0, 2:
			return []model.ToolCall{{ID: fmt.Sprintf("search-%d", i), Name: tool.ToolSearchToolName, Args: `{"query":"live__read"}`}}
		case 1:
			return []model.ToolCall{{ID: "old-pending", Name: tool.MCPCallToolName, Args: `{"tool_name":"live__read","arguments":{"value":1}}`}}
		case 3:
			return []model.ToolCall{{ID: "new-version", Name: tool.MCPCallToolName, Args: `{"tool_name":"live__read","arguments":{"value":"v2"}}`}}
		case 4:
			if err := manager.RevokeServer("fixture", "live"); err != nil {
				t.Fatal(err)
			}
			return []model.ToolCall{{ID: "revoked", Name: tool.MCPCallToolName, Args: `{"tool_name":"live__read","arguments":{"value":"v2"}}`}}
		}
		return nil
	}}
	approvals := 0
	requester := approvalRequesterFunc(func(_ context.Context, r agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		approvals++
		if r.Tool.Name != "live__read" || r.Call.Name != r.Tool.Name || r.Call.ModelCall == nil {
			t.Fatal("approval lost real target")
		}
		if approvals == 1 {
			publish("string")
			if err := manager.RefreshTools(ctx, "fixture", "live"); err != nil {
				t.Fatal(err)
			}
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
	})
	grants, err := NewMCPGrantStore(filepath.Join(t.TempDir(), "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test", MCPGrants: grants})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "read", ApprovalRequester: requester, AgentSpec: agent.AgentSpec{Model: model, Tools: []tool.Tool{toolsearch.NewSource(manager, exactMCPRanker{})}, DeferredTools: manager}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 1 || approvals != 2 {
		events, _ := service.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
		for _, e := range events {
			if e.Tool != nil && e.Type == session.EventTypeToolResult {
				t.Logf("%s %s %+v", e.Tool.Name, e.Tool.Status, e.Tool.Output)
			}
		}
		t.Fatalf("effects=%d approvals=%d", effects.Load(), approvals)
	}
	loaded, err := service.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, e := range loaded {
		if e.Type == session.EventTypeToolResult && e.Tool != nil {
			statuses[e.Tool.ID] = e.Tool.Status
		}
	}
	if statuses["old-pending"] != "failed" || statuses["new-version"] != "completed" || statuses["revoked"] != "failed" {
		t.Fatalf("statuses=%v", statuses)
	}
}

func TestMCPFixedLegacySessionRequiresRediscovery(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "legacy", Workspace: session.WorkspaceRef{Key: "legacy", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	old := model.ToolCall{ID: "historical-search", Name: "ToolSearch", Args: `{"query":"docs"}`}
	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{old}, "")
	for _, e := range []*session.Event{{Type: session.EventTypeToolCall, Message: &message, Tool: &session.EventTool{ID: old.ID, Name: old.Name, Input: map[string]any{"query": "docs"}}}, {Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: old.ID, Name: old.Name, Status: "completed", Output: map[string]any{"tools": []any{map[string]any{"name": "docs__read"}}}}, Meta: map[string]any{"caelis.tool.discovered_names": []string{"docs__read"}, "caelis.tool_exposure.v1": map[string]any{"active": []string{"docs__read"}}}}} {
		if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: e}); err != nil {
			t.Fatal(err)
		}
	}
	reloaded := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	calls := 0
	remote := tool.NamedTool{Def: tool.Definition{Name: "docs__read", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataPluginID: "fixture", tool.MetadataMCPServer: "docs", tool.MetadataMCPTool: "read", tool.MetadataMCPSourceFingerprint: "current", tool.MetadataMCPSourceTrusted: true}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		calls++
		return tool.Result{Content: []model.Part{model.NewTextPart("read")}}, nil
	}}
	source := &runtimeDeferredSource{}
	source.publish(remote)
	llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		call := model.ToolCall{ID: fmt.Sprintf("new-%d", i), Name: tool.MCPCallToolName, Args: `{"tool_name":"docs__read","arguments":{}}`}
		switch i {
		case 0, 2:
		case 1:
			call.Name = tool.ToolSearchToolName
			call.Args = `{"query":"docs__read"}`
		default:
			return nil
		}
		return []model.ToolCall{call}
	}}
	rt, err := New(Config{Sessions: reloaded, AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "continue", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(source, exactMCPRanker{})}, DeferredTools: source}})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("legacy discovery granted %d calls", calls)
	}
	if _, err := rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}}); err != nil {
		t.Fatal(err)
	}
	resume := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i > 0 {
			return nil
		}
		return []model.ToolCall{{ID: "after-compact", Name: tool.MCPCallToolName, Args: `{"tool_name":"docs__read","arguments":{}}`}}
	}}
	continued, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "continue after compact", AgentSpec: agent.AgentSpec{Model: resume, Tools: []tool.Tool{toolsearch.NewSource(source, exactMCPRanker{})}, DeferredTools: source}})
	if err != nil {
		t.Fatal(err)
	}
	if err := continued.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("canonical discovery did not survive compaction")
	}

	events, err := reloaded.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, e := range events {
		if e.Type == session.EventTypeToolResult && e.Tool != nil {
			statuses[e.Tool.ID] = e.Tool.Status
		}
	}
	if statuses["new-0"] != "failed" || statuses["new-2"] != "completed" {
		t.Fatalf("legacy resume=%v", statuses)
	}
}

func TestMCPFixedUnknownRecoveryKeepsOuterCallWithoutReplay(t *testing.T) {
	service, active := newJournalTestSession(t, "fixed-unknown")
	original := model.ToolCall{ID: "provider-operation", Name: tool.MCPCallToolName, Args: `{"tool_name":"docs__read","arguments":{"number":9007199254740993}}`}
	writer := journaledTool{sessions: service, sessionRef: active.SessionRef, now: time.Now}
	record := session.NormalizeToolExecution(session.ToolExecution{Schema: session.ToolExecutionSchemaVersion, Key: session.ExecutionKey{SessionID: active.SessionID, RunID: "original-run", TurnID: "original-turn", StepID: "original-step", ToolCallID: original.ID}, Revision: 1, ToolName: "docs__read", Input: json.RawMessage(`{"number":9007199254740993}`), ModelCall: &original, Binding: &tool.Binding{Name: "docs__read", Identity: "identity", Version: "v1"}, EffectClass: string(tool.EffectNonIdempotent), Status: session.ToolExecutionPrepared})
	if err := writer.appendEntry(t.Context(), session.ToolExecution{}, record, session.ExecutionRecord{}, session.ExecutionRecord{}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []session.ToolExecutionStatus{session.ToolExecutionApproved, session.ToolExecutionStarted} {
		previous := record
		record.Revision++
		record.Status = status
		if err := writer.appendEntry(t.Context(), previous, record, session.ExecutionRecord{}, session.ExecutionRecord{}); err != nil {
			t.Fatal(err)
		}
	}
	rt := &Runtime{sessions: service, clock: time.Now}
	effects := 0
	remote := tool.NamedTool{Def: tool.Definition{Name: "docs__read"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects++; return tool.Result{}, nil }}
	if err := rt.recoverIncompleteToolExecutions(t.Context(), active.SessionRef, remote); err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if effects != 0 || last.Journal.ToolExecution.Status != session.ToolExecutionUnknownOutcome || last.Tool.Name != "docs__read" || last.Tool.ModelCall == nil || last.Message.ToolResults()[0].Name != tool.MCPCallToolName || fmt.Sprint(last.Tool.Input["number"]) != "9007199254740993" {
		t.Fatalf("unknown recovery changed identity or replayed: effects=%d event=%+v", effects, last.Tool)
	}
}

func TestMCPFixedCancellationKeepsOriginalOperation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	entered, stopped := make(chan struct{}), make(chan struct{})
	var effects atomic.Int32
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "cancel", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "wait", Description: "Wait for cancellation", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		effects.Add(1)
		close(entered)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	defer endpoint.Close()
	manager, err := mcp.NewManager(ctx, []mcp.ServerSpec{{PluginID: "fixture", Name: "live", Transport: mcp.TransportStreamableHTTP, URL: endpoint.URL, SourceTrusted: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	<-manager.Initialized()
	root := t.TempDir()
	service := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "fixture", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}, PolicyRegistry: mcpFixturePolicy(), DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "search", Name: tool.ToolSearchToolName, Args: `{"query":"live__wait"}`}}
		}
		if i == 1 {
			return []model.ToolCall{{ID: "original-remote-operation", Name: tool.MCPCallToolName, Args: `{"tool_name":"live__wait","arguments":{}}`}}
		}
		return nil
	}}
	result, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "wait", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(manager, exactMCPRanker{})}, DeferredTools: manager}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !result.Handle.Cancel().Cancelled() {
		t.Fatal("cancel was not admitted")
	}
	if err := result.Handle.WaitCompletion(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("MCP transport did not propagate cancellation")
	}
	fresh, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.recoverIncompleteToolExecutions(ctx, active.SessionRef, manager.Tools()...); err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var latest *session.ToolExecution
	cancelRequested := false
	for _, e := range events {
		if e.Journal != nil && e.Journal.ToolExecution != nil && e.Journal.ToolExecution.ToolName == "live__wait" {
			r := e.Journal.ToolExecution
			latest = r
			cancelRequested = cancelRequested || r.Status == session.ToolExecutionCancelRequested
			if r.Key.ToolCallID != "original-remote-operation" || r.ModelCall == nil || r.ModelCall.ID != r.Key.ToolCallID {
				t.Fatal("cancellation changed operation identity")
			}
		}
	}
	if effects.Load() != 1 || !cancelRequested || latest == nil || latest.Status == session.ToolExecutionSucceeded {
		t.Fatalf("effects=%d cancellation=%v latest=%+v", effects.Load(), cancelRequested, latest)
	}
}
