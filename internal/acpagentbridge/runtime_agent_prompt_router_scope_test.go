package acpagentbridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestRuntimeAgentPromptRouterKeepsSiblingNarrativesAndMainFinalReplayDistinct(t *testing.T) {
	turn := newTestControlTurn(
		scopedNarrativeEnvelope(eventstream.ScopeMain, "root", acp.UpdateAgentMessage, "main-message", "main live", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-a", acp.UpdateAgentMessage, "child-message", "shared message", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-b", acp.UpdateAgentMessage, "child-message", "shared message", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-a", acp.UpdateAgentMessage, "child-message-2", "shared message", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-a", acp.UpdateAgentMessage, "child-message", "shared message one", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-b", acp.UpdateAgentMessage, "child-message", "shared message two", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-a", acp.UpdateAgentThought, "", "shared thought", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-b", acp.UpdateAgentThought, "", "shared thought", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-a", acp.UpdateAgentThought, "", "shared thought one", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-b", acp.UpdateAgentThought, "", "shared thought two", false),
		eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			Scope:     eventstream.ScopeSubagent,
			ScopeID:   "task-b",
			Update: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo,
				ToolCallID:    "child-tool-1",
			},
		},
		scopedNarrativeEnvelope(eventstream.ScopeMain, "root", acp.UpdateAgentMessage, "main-message", "main live", true),
	)
	runtimeAgent, sessionID := newPromptRouterAgentForScopeTest(t, turn)
	cb := &recordingPromptCallbacks{}
	promptRouterTurnForScopeTest(t, runtimeAgent, sessionID, cb)

	if got, want := agentMessageChunks(cb.notifications), []string{
		"main live", "shared message", "shared message", "shared message", " one", " two",
	}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("agent message chunks = %#v, want sibling narratives once each and no main final replay %#v", got, want)
	}
	if got, want := agentThoughtChunks(cb.notifications), []string{
		"shared thought", "shared thought", " one", " two",
	}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("agent thought chunks = %#v, want independent sibling narrative deltas %#v", got, want)
	}
}

func TestRuntimeAgentPromptRouterChildPermissionKeepsOtherNarrativeReplayState(t *testing.T) {
	turn := newTestControlTurn(
		scopedNarrativeEnvelope(eventstream.ScopeMain, "root", acp.UpdateAgentMessage, "main-message", "main live", false),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-b", acp.UpdateAgentMessage, "sibling-message", "sibling live", false),
		scopedPermissionEnvelope("task-a"),
		scopedNarrativeEnvelope(eventstream.ScopeMain, "root", acp.UpdateAgentMessage, "main-message", "main live", true),
		scopedNarrativeEnvelope(eventstream.ScopeSubagent, "task-b", acp.UpdateAgentMessage, "sibling-message", "sibling live", true),
	)
	runtimeAgent, sessionID := newPromptRouterAgentForScopeTest(t, turn)
	cb := &permissionCountingCallbacks{}
	promptRouterTurnForScopeTest(t, runtimeAgent, sessionID, cb)

	if cb.permissions != 1 {
		t.Fatalf("permission requests = %d, want one child request", cb.permissions)
	}
	if got, want := agentMessageChunks(cb.notifications), []string{"main live", "sibling live"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("agent message chunks = %#v, want child permission to preserve main and sibling replay state %#v", got, want)
	}
}

func TestRuntimeAgentPromptRouterKeepsSiblingTerminalOutputsWithSharedToolIDs(t *testing.T) {
	completed := acp.ToolStatusCompleted
	output := "shared child terminal output\n"
	turn := newTestControlTurn(
		scopedTerminalEnvelope("task-a", "shared-tool", "shared-terminal", completed, output),
		scopedTerminalEnvelope("task-b", "shared-tool", "shared-terminal", completed, output),
	)
	runtimeAgent, sessionID := newPromptRouterAgentForScopeTest(t, turn)
	cb := &recordingPromptCallbacks{}
	promptRouterTurnForScopeTest(t, runtimeAgent, sessionID, cb)

	if got, want := terminalOutputPayloads(cb.notifications, "shared-tool"), []string{output, output}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("terminal outputs = %#v, want independent sibling outputs %#v", got, want)
	}
}

func TestRuntimeAgentPromptDirectPathScopesNarrativesAndPermissionReset(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtimeAgent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  directScopedNarrativeRuntime{},
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "direct-scope-test"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := runtimeAgent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &permissionCountingCallbacks{}
	if _, err := runtimeAgent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"run"}`)},
	}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	if cb.permissions != 1 {
		t.Fatalf("permission requests = %d, want one scoped child request", cb.permissions)
	}
	if got, want := agentMessageChunks(cb.notifications), []string{"main live", "shared child", "shared child"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("agent message chunks = %#v, want direct event scope to preserve main and sibling replay state %#v", got, want)
	}
}

func scopedNarrativeEnvelope(scope eventstream.Scope, scopeID string, updateType string, messageID string, text string, final bool) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     scope,
		ScopeID:   scopeID,
		Final:     final,
		Update: acp.ContentChunk{
			SessionUpdate: updateType,
			MessageID:     messageID,
			Content:       acp.TextContent{Type: "text", Text: text},
		},
	}
}

func scopedPermissionEnvelope(scopeID string) eventstream.Envelope {
	status := acp.ToolStatusPending
	return eventstream.Envelope{
		Kind:              eventstream.KindRequestPermission,
		SessionID:         "session-1",
		Scope:             eventstream.ScopeSubagent,
		ScopeID:           scopeID,
		ApprovalRequestID: "child-approval-1",
		Permission: &acp.RequestPermissionRequest{
			SessionID: "session-1",
			ToolCall: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo,
				ToolCallID:    "child-permission",
				Status:        &status,
			},
			Options: []acp.PermissionOption{{
				OptionID: acp.PermAllowOnce,
				Name:     "Allow once",
				Kind:     acp.PermAllowOnce,
			}},
		},
	}
}

func scopedTerminalEnvelope(scopeID string, toolCallID string, terminalID string, status string, output string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   scopeID,
		Final:     true,
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    toolCallID,
			Status:        &status,
			Meta:          metautil.WithTerminalOutput(nil, terminalID, output),
		},
	}
}

func newPromptRouterAgentForScopeTest(t *testing.T, turn *testControlTurn) (*runtimeacp.RuntimeAgent, string) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	router := &testPromptRouter{result: controlprompt.Result{Handled: true, Turn: turn}}
	runtimeAgent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := runtimeAgent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return runtimeAgent, activeSession.SessionID
}

func promptRouterTurnForScopeTest(t *testing.T, runtimeAgent *runtimeacp.RuntimeAgent, sessionID string, cb acp.PromptCallbacks) {
	t.Helper()
	if _, err := runtimeAgent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: sessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"/review"}`)},
	}, cb); err != nil {
		t.Fatalf("Prompt(/review) error = %v", err)
	}
}

type directScopedNarrativeRuntime struct{}

func (directScopedNarrativeRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	mainScope := &session.EventScope{TurnID: "turn-1"}
	childAScope := directSubagentScope("task-a")
	childBScope := directSubagentScope("task-b")
	sessionID := req.SessionRef.SessionID
	return agent.RunResult{
		Session: session.Session{SessionRef: req.SessionRef},
		Handle: terminalBridgeRun{events: []*session.Event{
			directNarrativeEvent(sessionID, "", mainScope, "main-message", "main live", session.VisibilityUIOnly),
			directNarrativeEvent(sessionID, "", childAScope, "child-message", "shared child", session.VisibilityUIOnly),
			directNarrativeEvent(sessionID, "", childBScope, "child-message", "shared child", session.VisibilityUIOnly),
			directPermissionEvent(sessionID, childAScope),
			directNarrativeEvent(sessionID, "child-b-final", childBScope, "child-message", "shared child", session.VisibilityCanonical),
			directNarrativeEvent(sessionID, "main-final", mainScope, "main-message", "main live", session.VisibilityCanonical),
		}},
	}, nil
}

func (directScopedNarrativeRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func directNarrativeEvent(sessionID string, eventID string, scope *session.EventScope, messageID string, text string, visibility session.Visibility) *session.Event {
	return &session.Event{
		ID:         eventID,
		SessionID:  sessionID,
		Type:       session.EventTypeAssistant,
		Visibility: visibility,
		Scope:      scope,
		Text:       text,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: acp.UpdateAgentMessage,
			MessageID:     messageID,
		}},
	}
}

func directPermissionEvent(sessionID string, scope *session.EventScope) *session.Event {
	return &session.Event{
		SessionID:  sessionID,
		Type:       session.EventTypeToolCall,
		Visibility: session.VisibilityUIOnly,
		Scope:      scope,
		Protocol: &session.EventProtocol{Permission: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{ID: "child-permission", Name: "WRITE", Status: "pending"},
			Options: []session.ProtocolApprovalOption{{
				ID: acp.PermAllowOnce, Name: "Allow once", Kind: acp.PermAllowOnce,
			}},
		}},
	}
}

func directSubagentScope(taskID string) *session.EventScope {
	return &session.EventScope{
		TurnID: "turn-1",
		Participant: session.ParticipantRef{
			ID:           "child-" + taskID,
			Kind:         session.ParticipantKindSubagent,
			DelegationID: taskID,
		},
	}
}
