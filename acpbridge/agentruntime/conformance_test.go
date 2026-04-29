package agentruntime_test

import (
	"context"
	"encoding/json"
	"iter"
	"slices"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/acp/fixture"
	"github.com/OnslaughtSnail/caelis/acpbridge/agentruntime"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	"github.com/OnslaughtSnail/caelis/sdk/runtime/agents/chat"
	"github.com/OnslaughtSnail/caelis/sdk/runtime/local"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	"github.com/OnslaughtSnail/caelis/sdk/session/inmemory"
)

func TestRuntimeAgentConformanceReplayOrdering(t *testing.T) {
	agent, sessions := newTestRuntimeAgent(t, staticModel{text: "ok"})
	ctx := context.Background()
	session, err := sessions.StartSession(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "/tmp/acp-fixture-load",
			CWD: "/tmp/acp-fixture-load",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	user := sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")
	if _, err := sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: session.SessionRef,
		Event: &sdksession.Event{
			Type:    sdksession.EventTypeUser,
			Message: &user,
			Text:    "hello",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}
	assistant := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "world")
	if _, err := sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: session.SessionRef,
		Event: &sdksession.Event{
			Type:    sdksession.EventTypeAssistant,
			Message: &assistant,
			Text:    "world",
			Protocol: &sdksession.EventProtocol{
				UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
			},
		},
	}); err != nil {
		t.Fatalf("AppendEvent(assistant) error = %v", err)
	}
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	if _, err := agent.LoadSession(ctx, acp.LoadSessionRequest{
		SessionID: session.SessionID,
		CWD:       session.CWD,
	}, rec); err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := fixture.UpdateKinds(rec.Notifications()), []string{acp.UpdateUserMessage, acp.UpdateAgentMessage}; !slices.Equal(got, want) {
		t.Fatalf("replay update kinds = %v, want %v", got, want)
	}
}

func TestRuntimeAgentConformancePromptOrdering(t *testing.T) {
	agent, _ := newTestRuntimeAgent(t, staticModel{text: "ok"})
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: resp.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"Reply with exactly: ok"}`)},
	}, rec); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	kinds := fixture.UpdateKinds(rec.Notifications())
	if len(kinds) < 2 {
		t.Fatalf("prompt update kinds = %v, want at least user + assistant", kinds)
	}
	if kinds[0] != acp.UpdateUserMessage {
		t.Fatalf("first prompt update = %q, want %q", kinds[0], acp.UpdateUserMessage)
	}
	if !slices.Contains(kinds, acp.UpdateAgentMessage) {
		t.Fatalf("prompt update kinds = %v, want assistant message update", kinds)
	}
}

func TestRuntimeAgentConformanceCancellation(t *testing.T) {
	agent, _ := newTestRuntimeAgent(t, cancelModel{})
	sessionResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	done := make(chan acp.PromptResponse, 1)
	errs := make(chan error, 1)
	go func() {
		resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionID: sessionResp.SessionID,
			Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"hang until cancelled"}`)},
		}, rec)
		if err != nil {
			errs <- err
			return
		}
		done <- resp
	}()
	time.Sleep(50 * time.Millisecond)
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionID: sessionResp.SessionID}); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	select {
	case err := <-errs:
		t.Fatalf("Prompt() error = %v, want cancelled response", err)
	case resp := <-done:
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonCancelled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt() did not return after cancellation")
	}
}

type staticModel struct{ text string }

func (m staticModel) Name() string { return "stub" }

func (m staticModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, m.text),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type cancelModel struct{}

func (cancelModel) Name() string { return "cancel-model" }

func (cancelModel) Generate(ctx context.Context, _ *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		<-ctx.Done()
		yield(nil, ctx.Err())
	}
}

func newTestRuntimeAgent(t *testing.T, model sdkmodel.LLM) (*agentruntime.RuntimeAgent, sdksession.Service) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime, err := local.New(local.Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely.",
		},
	})
	if err != nil {
		t.Fatalf("local.New() error = %v", err)
	}
	agent, err := agentruntime.New(agentruntime.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, sdksession.Session, acp.PromptRequest) (sdkruntime.AgentSpec, error) {
			return sdkruntime.AgentSpec{Name: "chat", Model: model}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acp.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("agentruntime.New() error = %v", err)
	}
	return agent, sessions
}
