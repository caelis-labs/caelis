package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixtureMCPDefinition(source string) tool.Definition {
	return tool.Definition{Name: "docs__read", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{
		tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataPluginID: "fixture",
		tool.MetadataMCPServer: "docs", tool.MetadataMCPTool: "read",
		tool.MetadataMCPSourceFingerprint: source,
	}}
}

func fixtureMCPWrapped(t *testing.T, grants *MCPGrantStore, active session.Session, def tool.Definition, choices []string, approvals, calls *atomic.Int32) policyWrappedTool {
	t.Helper()
	base := tool.NamedTool{Def: def, Invoke: func(_ context.Context, _ tool.Call) (tool.Result, error) {
		calls.Add(1)
		return tool.Result{Name: def.Name}, nil
	}}
	return policyWrappedTool{tool: base, policy: presets.WorkspaceWriteMode(), mode: presets.ModeWorkspaceWrite,
		session: active, sessionRef: active.SessionRef, mcpGrants: grants,
		approval: approvalContext{requester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			index := int(approvals.Add(1)) - 1
			if req.Approval == nil || len(req.Approval.Options) != 4 || req.Approval.ToolCall.Kind != "other" ||
				req.Approval.ToolCall.RawInput["value"] != "fixture" || req.Tool.Metadata[tool.MetadataMCPSourceFingerprint] != def.Metadata[tool.MetadataMCPSourceFingerprint] {
				t.Errorf("approval lost exact MCP identity, arguments or options: %#v", req)
			}
			want := []session.ProtocolApprovalOption{
				{ID: "allow_once", Name: "Allow Once", Kind: "allow_once"},
				{ID: "allow_session", Name: "Allow this session", Kind: "allow_once"},
				{ID: "allow_always", Name: "Allow Always", Kind: "allow_always"},
				{ID: "cancel", Name: "Cancel", Kind: "reject_once"},
			}
			if !reflect.DeepEqual(req.Approval.Options, want) {
				t.Errorf("options = %#v", req.Approval.Options)
			}
			if index >= len(choices) {
				t.Errorf("unexpected approval request %d", index)
				return agent.ApprovalResponse{}, nil
			}
			id := choices[index]
			if id == "failure" {
				return agent.ApprovalResponse{}, errors.New("Guardian unavailable")
			}
			return agent.ApprovalResponse{Outcome: "selected", OptionID: id, Approved: id != "cancel"}, nil
		})},
	}
}

func callFixtureMCP(t *testing.T, wrapped policyWrappedTool) tool.Result {
	t.Helper()
	result, err := wrapped.Call(t.Context(), tool.Call{ID: "call", Name: "docs__read", Input: []byte(`{"value":"fixture"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMCPApprovalScopesAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control", "mcp-grants.json")
	grants, err := NewMCPGrantStore(path)
	if err != nil {
		t.Fatal(err)
	}
	active := session.Session{SessionRef: session.SessionRef{SessionID: "session-one"}, CWD: t.TempDir(), CreatedAt: time.Now()}
	other := active
	other.SessionID = "session-two"
	def := fixtureMCPDefinition("source-one")
	var approvals, calls atomic.Int32

	once := fixtureMCPWrapped(t, grants, active, def, []string{"allow_once", "cancel"}, &approvals, &calls)
	callFixtureMCP(t, once)
	if calls.Load() != 1 {
		t.Fatalf("once calls = %d", calls.Load())
	}
	if result := callFixtureMCP(t, once); !result.IsError || calls.Load() != 1 || approvals.Load() != 2 {
		t.Fatalf("cancel executed remote: result=%#v calls=%d approvals=%d", result, calls.Load(), approvals.Load())
	}

	approvals.Store(0)
	sessionWrapped := fixtureMCPWrapped(t, grants, active, def, []string{"allow_session", "cancel"}, &approvals, &calls)
	callFixtureMCP(t, sessionWrapped)
	callFixtureMCP(t, sessionWrapped) // a later Turn of the same Session
	if calls.Load() != 3 || approvals.Load() != 1 {
		t.Fatalf("session calls=%d approvals=%d", calls.Load(), approvals.Load())
	}
	reopened, err := NewMCPGrantStore(path)
	if err != nil {
		t.Fatal(err)
	}
	callFixtureMCP(t, fixtureMCPWrapped(t, reopened, active, def, nil, &approvals, &calls))
	if calls.Load() != 4 || approvals.Load() != 1 {
		t.Fatalf("restart lost session grant: calls=%d approvals=%d", calls.Load(), approvals.Load())
	}
	approvals.Store(0)
	if result := callFixtureMCP(t, fixtureMCPWrapped(t, reopened, other, def, []string{"cancel"}, &approvals, &calls)); !result.IsError || calls.Load() != 4 {
		t.Fatalf("session grant inherited: result=%#v calls=%d", result, calls.Load())
	}
	replacement := active
	replacement.CreatedAt = active.CreatedAt.Add(time.Second)
	approvals.Store(0)
	if result := callFixtureMCP(t, fixtureMCPWrapped(t, reopened, replacement, def, []string{"cancel"}, &approvals, &calls)); !result.IsError || calls.Load() != 4 {
		t.Fatalf("new Session with reused ID inherited grant: result=%#v calls=%d", result, calls.Load())
	}

	approvals.Store(0)
	callFixtureMCP(t, fixtureMCPWrapped(t, reopened, other, def, []string{"allow_always"}, &approvals, &calls))
	third := active
	third.SessionID = "session-three"
	callFixtureMCP(t, fixtureMCPWrapped(t, reopened, third, def, nil, &approvals, &calls))
	reopened, err = NewMCPGrantStore(path)
	if err != nil {
		t.Fatal(err)
	}
	callFixtureMCP(t, fixtureMCPWrapped(t, reopened, third, def, nil, &approvals, &calls))
	if calls.Load() != 7 || approvals.Load() != 1 {
		t.Fatalf("always calls=%d approvals=%d", calls.Load(), approvals.Load())
	}
	grant, err := mcpGrantFor(def, active)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Revoke(grant, "always", ""); err != nil {
		t.Fatal(err)
	}
	approvals.Store(0)
	if result := callFixtureMCP(t, fixtureMCPWrapped(t, reopened, third, def, []string{"cancel"}, &approvals, &calls)); !result.IsError || calls.Load() != 7 {
		t.Fatalf("revoke failed: result=%#v calls=%d", result, calls.Load())
	}
	// A changed connection, tool definition or schema has a new source hash.
	approvals.Store(0)
	if result := callFixtureMCP(t, fixtureMCPWrapped(t, reopened, active, fixtureMCPDefinition("source-two"), []string{"cancel"}, &approvals, &calls)); !result.IsError || calls.Load() != 7 {
		t.Fatalf("changed source inherited grant: result=%#v calls=%d", result, calls.Load())
	}
}

func TestMCPApprovalFailureAndInvalidDecisionNeverCallRemote(t *testing.T) {
	active := session.Session{SessionRef: session.SessionRef{SessionID: "session"}, CWD: t.TempDir(), CreatedAt: time.Now()}
	var approvals, calls atomic.Int32
	for _, choice := range []string{"failure", "cancel", "unknown"} {
		approvals.Store(0)
		readOnly := fixtureMCPDefinition("source")
		readOnly.EffectClass = tool.EffectReadOnly
		readOnly.Metadata["annotations"] = map[string]any{"readOnlyHint": true}
		wrapped := fixtureMCPWrapped(t, nil, active, readOnly, []string{choice}, &approvals, &calls)
		_, _ = wrapped.Call(t.Context(), tool.Call{ID: "call", Name: "docs__read", Input: []byte(`{"value":"fixture"}`)})
		if calls.Load() != 0 || approvals.Load() != 1 {
			t.Fatalf("choice=%s calls=%d approvals=%d", choice, calls.Load(), approvals.Load())
		}
	}
}

func TestMCPApprovalOverridesExplicitFullAccessPolicy(t *testing.T) {
	active := session.Session{SessionRef: session.SessionRef{SessionID: "session"}, CWD: t.TempDir(), CreatedAt: time.Now()}
	var approvals, calls atomic.Int32
	wrapped := fixtureMCPWrapped(t, nil, active, fixtureMCPDefinition("source"), []string{"cancel"}, &approvals, &calls)
	wrapped.policy = presets.DangerFullAccessMode()
	wrapped.mode = presets.ModeDangerFullAccess
	result := callFixtureMCP(t, wrapped)
	if !result.IsError || approvals.Load() != 1 || calls.Load() != 0 {
		t.Fatalf("full access bypassed MCP gate: result=%#v approvals=%d calls=%d", result, approvals.Load(), calls.Load())
	}
}

func TestMCPGrantStoreConcurrentScopesSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	store, err := NewMCPGrantStore(path)
	if err != nil {
		t.Fatal(err)
	}
	const count = 8
	var wg sync.WaitGroup
	errorsCh := make(chan error, count)
	for index := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			grant := MCPGrant{Workspace: "/workspace", PluginID: "fixture", Server: "docs", Tool: strconv.Itoa(index), Projected: "docs__" + strconv.Itoa(index), Source: "v1", SessionEpoch: "fixture-created"}
			if err := store.Grant(grant, "always", ""); err != nil {
				errorsCh <- err
				return
			}
			if allowed, err := store.Allows(grant, "session"); err != nil || !allowed {
				errorsCh <- errors.Join(err, errors.New("committed grant missing"))
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	reopened, err := NewMCPGrantStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for index := range count {
		grant := MCPGrant{Workspace: "/workspace", PluginID: "fixture", Server: "docs", Tool: strconv.Itoa(index), Projected: "docs__" + strconv.Itoa(index), Source: "v1", SessionEpoch: "fixture-created"}
		allowed, err := reopened.Allows(grant, "another-session")
		if err != nil || !allowed {
			t.Fatalf("grant %d lost after concurrent writes/restart: %v", index, err)
		}
	}
}

func TestMCPApprovalFixtureProcess(t *testing.T) {
	if os.Getenv("CAELIS_APPROVAL_MCP_FIXTURE") != "1" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "approval-fixture", Version: "1"}, nil)
	count := 0
	server.AddTool(&mcpsdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		count++
		if err := os.WriteFile(os.Getenv("CAELIS_APPROVAL_MCP_COUNT"), []byte(strconv.Itoa(count)), 0o600); err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "fixture read"}}}, nil
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestRuntimeMCPToolSearchApprovalBeforeLocalServerCall(t *testing.T) {
	root := t.TempDir()
	countPath := filepath.Join(root, "remote-count")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	mgr, err := mcp.NewManager(ctx, []mcp.ServerSpec{{PluginID: "fixture", Name: "docs", Command: os.Args[0], Args: []string{"-test.run=^TestMCPApprovalFixtureProcess$"}, WorkDir: root, CleanEnvironment: true, Env: map[string]string{"CAELIS_APPROVAL_MCP_FIXTURE": "1", "CAELIS_APPROVAL_MCP_COUNT": countPath}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	select {
	case <-mgr.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	ready := mgr.Tools()
	if len(ready) != 1 || !tool.IsMCPDefinition(ready[0].Definition()) {
		t.Fatalf("ready MCP tools = %#v", ready)
	}
	service := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions"), SessionIDGenerator: func() string { return "approval-runtime" }})
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "fixture", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "fixture", PreferredSessionID: "approval-other", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	third, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "fixture", PreferredSessionID: "approval-third", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewMCPGrantStore(filepath.Join(root, "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}, MCPGrants: grants})
	if err != nil {
		t.Fatal(err)
	}
	alwaysTokenID := ""
	for _, tc := range []struct {
		option    string
		active    session.Session
		wantCalls string
		approvals int
	}{
		{"cancel", active, "", 1},
		{"allow_once", active, "1", 1},
		{"allow_session", active, "2", 1},
		{"", active, "3", 0},
		{"cancel", other, "3", 1},
		{"allow_always", other, "4", 1},
		{"", third, "5", 0},
	} {
		source := &runtimeDeferredSource{}
		llm := &deferredRuntimeModel{source: source, late: ready[0]}
		approvals := 0
		pauseTokenID := ""
		run, err := rt.Run(ctx, agent.RunRequest{SessionRef: tc.active.SessionRef, Input: "read docs", ApprovalRequester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			approvals++
			pauseTokenID = req.PauseTokenID
			if tc.option == "allow_always" {
				alwaysTokenID = req.PauseTokenID
			}
			if tc.option == "" {
				return agent.ApprovalResponse{}, errors.New("existing MCP grant did not suppress approval")
			}
			if req.Approval == nil || len(req.Approval.Options) != 4 {
				t.Errorf("Runtime approval = %#v", req.Approval)
			}
			return agent.ApprovalResponse{Outcome: "selected", OptionID: tc.option, Approved: tc.option != "cancel"}, nil
		}), AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(source, toolsearch.NewLexicalRanker())}, DeferredTools: source}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := drainRunnerEvents(t, run.Handle); err != nil {
			t.Fatal(err)
		}
		if approvals != tc.approvals {
			t.Fatalf("option=%s approvals=%d", tc.option, approvals)
		}
		if tc.approvals == 1 {
			token, err := rt.pauseToken(ctx, tc.active.SessionRef, pauseTokenID)
			if err != nil || token.Status != session.PauseTokenResolved || token.OptionID != tc.option || token.Approval == nil || len(token.Approval.Options) != 4 {
				t.Fatalf("durable MCP approval = %#v, %v", token, err)
			}
		}
		raw, err := os.ReadFile(countPath)
		if tc.wantCalls == "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || string(raw) != tc.wantCalls {
			t.Fatalf("option=%s remote count=%q err=%v", tc.option, raw, err)
		}
	}
	// Reopen canonical Session and grant files as a fresh Runtime activation.
	restartedSessions := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	restartedGrants, err := NewMCPGrantStore(filepath.Join(root, "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(Config{Sessions: restartedSessions, AgentFactory: chat.Factory{}, MCPGrants: restartedGrants})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := restarted.pauseToken(ctx, other.SessionRef, alwaysTokenID)
	if err != nil || retained.Status != session.PauseTokenResolved || retained.OptionID != "allow_always" || retained.Approval == nil || len(retained.Approval.Options) != 4 {
		t.Fatalf("restarted approval journal = %#v, %v", retained, err)
	}
	last, err := restartedSessions.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "fixture", PreferredSessionID: "approval-after-restart", Workspace: session.WorkspaceRef{Key: "fixture", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	source := &runtimeDeferredSource{}
	llm := &deferredRuntimeModel{source: source, late: ready[0]}
	run, err := restarted.Run(ctx, agent.RunRequest{SessionRef: last.SessionRef, Input: "read docs", ApprovalRequester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		return agent.ApprovalResponse{}, errors.New("restart lost durable Always grant")
	}), AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(source, toolsearch.NewLexicalRanker())}, DeferredTools: source}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(countPath); err != nil || string(raw) != "6" {
		t.Fatalf("restart remote count=%q err=%v", raw, err)
	}
}
