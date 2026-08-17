package acpsurface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/gatewayapptest"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestNewFromClientsRoutesStatusSlashThroughSharedPromptRouter(t *testing.T) {
	workdir := t.TempDir()
	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatalf("newTestAgentFromStack() error = %v", err)
	}
	session, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: workdir})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingCallbacks{}
	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: session.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/status"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/status) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if got := cb.firstAgentMessage(); !strings.Contains(got, "Model:") || !strings.Contains(got, "Session:") {
		t.Fatalf("agent message = %q, want status output", got)
	}
}

func TestNewFromClientsRunCommandPublishesTerminalBytesOnce(t *testing.T) {
	const output = "异步任务执行完毕，耗时 6 秒\n"
	var providerCalls atomic.Int32
	provider := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		call := providerCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if call == 1 {
			writeACPAgentTestSSE(w, map[string]any{
				"id": "run-command-1", "object": "chat.completion.chunk", "model": "run-command-test",
				"choices": []map[string]any{{
					"index": 0,
					"delta": map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{{
							"index": 0, "id": "call-shell", "type": "function",
							"function": map[string]any{
								"name": "RunCommand", "arguments": `{"command":"sleep 0.2; printf '异步任务执行完毕，耗时 6 秒\\n'","sandbox_permissions":"require_escalated","justification":"verify terminal output projection"}`,
							},
						}},
					},
				}},
			})
			writeACPAgentTestSSE(w, map[string]any{
				"id": "run-command-1", "object": "chat.completion.chunk", "model": "run-command-test",
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
			})
		} else {
			writeACPAgentTestSSE(w, map[string]any{
				"id": "run-command-2", "object": "chat.completion.chunk", "model": "run-command-test",
				"choices": []map[string]any{{
					"index": 0, "delta": map[string]any{"role": "assistant", "content": "done"}, "finish_reason": "stop",
				}},
			})
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	workspace := t.TempDir()
	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName: "caelis", UserID: "acpagent-test", StoreDir: t.TempDir(),
		WorkspaceKey: workspace, WorkspaceCWD: workspace, ApprovalMode: "manual",
		SkillDirs: []string{t.TempDir()}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
		Model: gatewayapp.ModelConfig{
			Provider: "openai-compatible", API: providers.APIOpenAICompatible,
			Model: "run-command-test", BaseURL: provider.URL, HTTPClient: provider.Client(),
			Token: "run-command-token", AuthType: model.AuthBearerToken, Timeout: 2 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatal(err)
	}
	active, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	callbacks := &allowingRecordingCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: active.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"run command"}`)},
	}, callbacks); err != nil {
		t.Fatal(err)
	}
	if got := acpTerminalOutputText(callbacks.notifications, "call-shell"); got != output {
		t.Fatalf("RunCommand terminal output = %q, want one exact delivery %q; notifications = %#v", got, output, callbacks.notifications)
	}
	for _, notification := range callbacks.notifications {
		var meta map[string]any
		switch update := notification.Update.(type) {
		case acp.ToolCall:
			if strings.TrimSpace(update.ToolCallID) == "call-shell" {
				meta = update.Meta
			}
		case acp.ToolCallUpdate:
			if strings.TrimSpace(update.ToolCallID) == "call-shell" {
				meta = update.Meta
			}
		}
		if delta, _ := metautil.RuntimeSection(meta, metautil.RuntimeTask)[metautil.RuntimeOutputDelta].(string); delta != "" {
			t.Fatalf("RunCommand live final exposed a second terminal byte source %q; notification = %#v", delta, notification)
		}
	}
	if err := stack.Close(); err != nil {
		t.Fatalf("stack.Close() = %v", err)
	}
}

func writeACPAgentTestSSE(w http.ResponseWriter, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func acpTerminalOutputText(notifications []acp.SessionNotification, callID string) string {
	var out strings.Builder
	for _, notification := range notifications {
		var meta map[string]any
		switch update := notification.Update.(type) {
		case acp.ToolCall:
			if strings.TrimSpace(update.ToolCallID) == callID {
				meta = update.Meta
			}
		case acp.ToolCallUpdate:
			if strings.TrimSpace(update.ToolCallID) == callID {
				meta = update.Meta
			}
		}
		if output, ok := metautil.TerminalOutput(meta); ok {
			out.WriteString(output.Data)
		}
	}
	return out.String()
}

func TestNewFromClientsStatusSlashUsesClientWorkspaceSession(t *testing.T) {
	ctx := context.Background()
	stackWorkspace := t.TempDir()
	clientWorkspace := t.TempDir()
	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: stackWorkspace,
		WorkspaceCWD: stackWorkspace,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatalf("newTestAgentFromStack() error = %v", err)
	}
	session, err := agent.NewSession(ctx, acp.NewSessionRequest{CWD: clientWorkspace})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingCallbacks{}
	resp, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionID: session.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/status"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/status) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	message := cb.firstAgentMessage()
	canonicalClientWorkspace, err := filepath.EvalSymlinks(clientWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	clientWorkspaceDisplay := canonicalClientWorkspace
	if !strings.Contains(message, "Workspace: "+clientWorkspaceDisplay) {
		t.Fatalf("status output = %q, want client workspace %q", message, clientWorkspaceDisplay)
	}
	stackWorkspaceDisplay := stackWorkspace
	if strings.Contains(message, "Workspace: "+stackWorkspaceDisplay) {
		t.Fatalf("status output = %q, should not use stack workspace %q", message, stackWorkspaceDisplay)
	}
}

func TestNewFromClientsHidesManagedSubagentSessionFromResume(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	provider := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"managed-child-1\",\"object\":\"chat.completion.chunk\",\"model\":\"managed-child-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"managed child ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
		Model: gatewayapp.ModelConfig{
			Provider: "openai-compatible", API: providers.APIOpenAICompatible,
			Model: "managed-child-test", BaseURL: provider.URL, HTTPClient: provider.Client(),
			Token: "managed-child-token", AuthType: model.AuthBearerToken, Timeout: 2 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := agent.NewSession(ctx, acp.NewSessionRequest{CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	meta := metautil.WithCompactRuntimeSection(nil, metautil.RuntimeSession, map[string]any{
		metautil.RuntimeSessionKind:     metautil.RuntimeSessionKindSubagent,
		metautil.RuntimeSessionParentID: ordinary.SessionID,
		metautil.RuntimeTaskID:          "task-1",
	})
	child, err := agent.NewSession(ctx, acp.NewSessionRequest{CWD: workspace, Meta: meta})
	if err != nil {
		t.Fatal(err)
	}
	childSession, err := stack.Sessions().Session(ctx, session.SessionRef{SessionID: child.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if childSession.Metadata["system_managed_agent"] != "subagent" {
		t.Fatalf("child Session metadata = %#v, want managed subagent marker", childSession.Metadata)
	}
	foreign, err := stack.Sessions().StartSession(ctx, session.StartSessionRequest{
		AppName: stack.AppName(),
		UserID:  stack.UserID(),
		Workspace: session.WorkspaceRef{
			Key: workspace,
			CWD: workspace,
		},
		Metadata: session.CloneState(childSession.Metadata),
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignBefore := foreign
	if _, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionID: foreign.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"must not run"}`)},
	}, &recordingCallbacks{}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Prompt(foreign system-managed child) error = %v, want session not found", err)
	}
	if _, err := agent.SessionMessage(ctx, acp.SessionMessageRequest{
		SessionID: foreign.SessionID, MessageID: "foreign-message", From: "main", To: "parent", Message: "must not deliver",
	}, &recordingCallbacks{}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("SessionMessage(foreign system-managed child) error = %v, want session not found", err)
	}
	if _, err := agent.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionID: foreign.SessionID, ModeID: "manual",
	}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("SetSessionMode(foreign system-managed child) error = %v, want session not found", err)
	}
	if _, err := agent.CloseSession(ctx, acp.CloseSessionRequest{SessionID: foreign.SessionID}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("CloseSession(foreign system-managed child) error = %v, want session not found", err)
	}
	foreignAfter, err := stack.Sessions().Session(ctx, foreign.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if foreignAfter.Revision != foreignBefore.Revision || !reflect.DeepEqual(foreignAfter.Metadata, foreignBefore.Metadata) {
		t.Fatalf("foreign managed Session changed: before=%#v after=%#v", foreignBefore, foreignAfter)
	}
	callbacks := &recordingCallbacks{}
	result, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionID: child.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"reply once"}`)},
	}, callbacks)
	if err != nil {
		t.Fatalf("Prompt(system-managed child) error = %v", err)
	}
	if result.StopReason != acp.StopReasonEndTurn || callbacks.firstAgentMessage() != "managed child ok" {
		t.Fatalf("Prompt(system-managed child) = %#v, message %q", result, callbacks.firstAgentMessage())
	}
	listed, err := agent.ListSessions(ctx, acp.SessionListRequest{CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != ordinary.SessionID {
		t.Fatalf("ListSessions() = %#v, want only ordinary Session %q", listed.Sessions, ordinary.SessionID)
	}
	if _, err := agent.LoadSession(ctx, acp.LoadSessionRequest{SessionID: child.SessionID, CWD: workspace}, &recordingCallbacks{}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("LoadSession(system-managed child) error = %v, want session not found", err)
	}
	if _, err := agent.ResumeSession(ctx, acp.ResumeSessionRequest{SessionID: child.SessionID, CWD: workspace}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("ResumeSession(system-managed child) error = %v, want session not found", err)
	}
}

func TestNewFromClientsUsesTypedSessionLifecycleAndPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workspace := t.TempDir()
	provider := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"acp-typed-1\",\"object\":\"chat.completion.chunk\",\"model\":\"typed-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"typed ACP ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: "acp-typed-workspace",
		WorkspaceCWD: workspace,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
		Model: gatewayapp.ModelConfig{
			Provider:   "openai-compatible",
			API:        providers.APIOpenAICompatible,
			Model:      "typed-test",
			BaseURL:    provider.URL,
			HTTPClient: provider.Client(),
			Token:      "typed-test-token",
			AuthType:   model.AuthBearerToken,
			Timeout:    2 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(ctx, acp.NewSessionRequest{CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := agent.ListSessions(ctx, acp.SessionListRequest{CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != created.SessionID {
		t.Fatalf("ListSessions() = %#v, want created Session", listed)
	}
	active, err := stack.Sessions().Session(ctx, session.SessionRef{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if active.WorkspaceKey != "acp-typed-workspace" {
		t.Fatalf("created WorkspaceKey = %q, want stable Host workspace key", active.WorkspaceKey)
	}
	callbacks := &recordingCallbacks{}
	result, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionID: created.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"reply once"}`)},
	}, callbacks)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != acp.StopReasonEndTurn || callbacks.firstAgentMessage() != "typed ACP ok" {
		t.Fatalf("Prompt() = %#v, message %q", result, callbacks.firstAgentMessage())
	}
	replayed := &recordingCallbacks{}
	if _, err := agent.LoadSession(ctx, acp.LoadSessionRequest{SessionID: created.SessionID, CWD: workspace}, replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.firstAgentMessage() != "typed ACP ok" {
		t.Fatalf("LoadSession replay = %q, want typed ACP output", replayed.firstAgentMessage())
	}
	if _, err := agent.CloseSession(ctx, acp.CloseSessionRequest{SessionID: created.SessionID}); err != nil {
		t.Fatal(err)
	}
	bound, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: stack.UserID()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bound.Prompt(ctx, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{OperationID: "prompt-after-acp-close", SessionID: created.SessionID},
		Input:     "must be rejected",
	})
	if !errorcode.Is(err, errorcode.FailedPrecondition) {
		t.Fatalf("Prompt after ACP close error = %v, want failed precondition", err)
	}
}

func TestNewFromClientsSetConfigOptionUsesNewSessionCWDWorkspace(t *testing.T) {
	ctx := context.Background()
	stackWorkspace := t.TempDir()
	clientWorkspace := t.TempDir()
	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: stackWorkspace,
		WorkspaceCWD: stackWorkspace,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Assembly: assembly.ResolvedAssembly{
			Modes: []assembly.ModeConfig{{ID: "manual", Name: "Manual"}},
			Configs: []assembly.ConfigOption{{
				ID: "tone", Name: "Tone", DefaultValue: "quiet",
				Options: []assembly.ConfigSelectOption{{Value: "quiet", Name: "Quiet"}, {Value: "loud", Name: "Loud"}},
			}},
		},
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatalf("newTestAgentFromStack() error = %v", err)
	}
	sessionResp, err := agent.NewSession(ctx, acp.NewSessionRequest{CWD: clientWorkspace})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		SessionID: sessionResp.SessionID,
		ConfigID:  "mode",
		Value:     "manual",
	}); err != nil {
		t.Fatalf("SetSessionConfigOption(mode) error = %v", err)
	}
	if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		SessionID: sessionResp.SessionID,
		ConfigID:  "model",
		Value:     requiredConfigOptionString(t, sessionResp.ConfigOptions, "model"),
	}); err != nil {
		t.Fatalf("SetSessionConfigOption(model) error = %v", err)
	}
	if value, ok := configOptionString(sessionResp.ConfigOptions, "reasoning_effort"); ok {
		if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			SessionID: sessionResp.SessionID,
			ConfigID:  "reasoning_effort",
			Value:     value,
		}); err != nil {
			t.Fatalf("SetSessionConfigOption(reasoning_effort) error = %v", err)
		}
	}
	if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		SessionID: sessionResp.SessionID,
		ConfigID:  "tone",
		Value:     "loud",
	}); err != nil {
		t.Fatalf("SetSessionConfigOption(tone) error = %v", err)
	}
	state, err := stack.SessionRuntimeState(ctx, session.SessionRef{
		AppName:      stack.AppName(),
		UserID:       stack.UserID(),
		SessionID:    sessionResp.SessionID,
		WorkspaceKey: clientWorkspace,
	})
	if err != nil {
		t.Fatalf("SessionRuntimeState(client workspace) error = %v", err)
	}
	if state.SessionMode != "auto-review" {
		t.Fatalf("client workspace approval mode = %q, want unchanged auto-review", state.SessionMode)
	}
	stored, err := stack.Sessions().SnapshotState(ctx, session.SessionRef{SessionID: sessionResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if got := assembly.CurrentModeID(stored); got != "manual" {
		t.Fatalf("client workspace app mode = %q, want manual", got)
	}
	if got := assembly.CurrentConfigValues(stored)["tone"]; got != "loud" {
		t.Fatalf("client workspace tone = %q, want loud", got)
	}
}

func TestNewFromClientsSetConfigOptionRoutesAdvertisedApprovalMode(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
		Model:        gatewayapp.ModelConfig{Provider: "ollama", Model: "llama3"},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatalf("newTestAgentFromStack() error = %v", err)
	}
	created, err := agent.NewSession(ctx, acp.NewSessionRequest{CWD: workspace})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if got := requiredConfigOptionString(t, created.ConfigOptions, "mode"); got != "auto-review" {
		t.Fatalf("NewSession() mode = %q, want auto-review", got)
	}
	if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		SessionID: created.SessionID,
		ConfigID:  "mode",
		Value:     "manual",
	}); err != nil {
		t.Fatalf("SetSessionConfigOption(mode) error = %v", err)
	}
	loaded, err := agent.LoadSession(ctx, acp.LoadSessionRequest{SessionID: created.SessionID, CWD: workspace}, &recordingCallbacks{})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got := requiredConfigOptionString(t, loaded.ConfigOptions, "mode"); got != "manual" {
		t.Fatalf("LoadSession() mode = %q, want manual", got)
	}
	resumed, err := agent.ResumeSession(ctx, acp.ResumeSessionRequest{SessionID: created.SessionID, CWD: workspace})
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if got := requiredConfigOptionString(t, resumed.ConfigOptions, "mode"); got != "manual" {
		t.Fatalf("ResumeSession() mode = %q, want manual", got)
	}
	state, err := stack.SessionRuntimeState(ctx, session.SessionRef{
		AppName: stack.AppName(), UserID: stack.UserID(), SessionID: created.SessionID, WorkspaceKey: workspace,
	})
	if err != nil {
		t.Fatalf("SessionRuntimeState() error = %v", err)
	}
	if state.SessionMode != "manual" {
		t.Fatalf("approval mode = %q, want manual", state.SessionMode)
	}
	stored, err := stack.Sessions().SnapshotState(ctx, session.SessionRef{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if got := assembly.CurrentModeID(stored); got != "" {
		t.Fatalf("app-owned mode = %q, want unchanged empty value", got)
	}
}

func requiredConfigOptionString(t *testing.T, options []acp.SessionConfigOption, id string) string {
	t.Helper()
	value, ok := configOptionString(options, id)
	if !ok {
		t.Fatalf("config option %q not found in %#v", id, options)
	}
	return value
}

func configOptionString(options []acp.SessionConfigOption, id string) (string, bool) {
	for _, option := range options {
		if strings.TrimSpace(option.ID) != id {
			continue
		}
		value, ok := option.CurrentValue.(string)
		return strings.TrimSpace(value), ok && strings.TrimSpace(value) != ""
	}
	return "", false
}

func newACPAgentTestStack(t *testing.T, cfg gatewayapp.Config) (*gatewayapp.Stack, error) {
	t.Helper()
	model := cfg.Model
	cfg.Model = gatewayapp.ModelConfig{}
	cfg.ResolveProviderHTTPClient = gatewayapptest.StaticProviderHTTPClient(model.HTTPClient)
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.Model) == "" {
		return stack, nil
	}
	if _, err := gatewayapptest.ConnectModel(context.Background(), stack, model); err != nil {
		_ = stack.Close()
		return nil, err
	}
	return stack, nil
}

type recordingCallbacks struct {
	notifications []acp.SessionNotification
}

type allowingRecordingCallbacks struct {
	recordingCallbacks
}

func (c *allowingRecordingCallbacks) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	}, nil
}

func (c *recordingCallbacks) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	c.notifications = append(c.notifications, notification)
	return nil
}

func (c *recordingCallbacks) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, nil
}

func (c *recordingCallbacks) firstAgentMessage() string {
	for _, notification := range c.notifications {
		chunk, ok := notification.Update.(acp.ContentChunk)
		if !ok || chunk.SessionUpdate != acp.UpdateAgentMessage {
			continue
		}
		content, ok := chunk.Content.(acp.TextContent)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(content.Text); text != "" {
			return text
		}
	}
	return ""
}
