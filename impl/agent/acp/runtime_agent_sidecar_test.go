package acp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	runtimeacp "github.com/OnslaughtSnail/caelis/impl/agent/acp"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
)

func TestRuntimeAgentPromptSlashCommandDetachesWhenParticipantIDMissing(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &sidecarLifecycleRuntime{sessions: sessions, attachWithoutParticipant: true}
	runtimeAgent, activeSession := newSidecarLifecycleAgent(t, runtime, sessions, func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
		return agent.AgentSpec{}, errors.New("main agent spec should not be built for side ACP slash command")
	})

	_, err := runtimeAgent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/helper inspect the repo"}`),
		},
	}, &recordingPromptCallbacks{})
	if err == nil {
		t.Fatal("Prompt(/helper) error = nil, want missing participant error")
	}
	if !strings.Contains(err.Error(), "was not attached") {
		t.Fatalf("Prompt(/helper) error = %v, want missing participant detail", err)
	}
	if runtime.detachCount != 1 {
		t.Fatalf("detachCount = %d, want 1", runtime.detachCount)
	}
	if runtime.detach.SessionRef.SessionID != activeSession.SessionID {
		t.Fatalf("detach request = %#v, want active session ref", runtime.detach)
	}
}

func TestRuntimeAgentPromptSlashCommandRejectsEmptyPrompt(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &sidecarLifecycleRuntime{sessions: sessions}
	runtimeAgent, activeSession := newSidecarLifecycleAgent(t, runtime, sessions, func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
		return agent.AgentSpec{}, errors.New("main agent spec should not be built for side ACP slash command")
	})

	_, err := runtimeAgent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/helper"}`),
		},
	}, &recordingPromptCallbacks{})
	if err == nil {
		t.Fatal("Prompt(/helper) error = nil, want usage error")
	}
	if !strings.Contains(err.Error(), "usage: /helper <prompt>") {
		t.Fatalf("Prompt(/helper) error = %v, want usage detail", err)
	}
	if runtime.attach.Agent != "" || runtime.runCalled {
		t.Fatalf("runtime attach=%#v runCalled=%v, want no runtime call for usage error", runtime.attach, runtime.runCalled)
	}
}

func TestRuntimeAgentPromptUnknownSlashCommandFallsThroughToMainRuntime(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &sidecarLifecycleRuntime{sessions: sessions}
	runtimeAgent, activeSession := newSidecarLifecycleAgent(t, runtime, sessions, func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
		return agent.AgentSpec{Name: "chat"}, nil
	})

	resp, err := runtimeAgent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/notregistered inspect the repo"}`),
		},
	}, &recordingPromptCallbacks{})
	if err != nil {
		t.Fatalf("Prompt(/notregistered) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if !runtime.runCalled {
		t.Fatal("main runtime Run was not called for unknown slash command")
	}
	if runtime.attach.Agent != "" {
		t.Fatalf("attach request = %#v, want no side ACP attach", runtime.attach)
	}
}

func TestRuntimeAgentPromptSlashCommandDetachesWhenPromptFails(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	promptErr := errors.New("side prompt failed")
	runtime := &sidecarLifecycleRuntime{sessions: sessions, promptErr: promptErr}
	runtimeAgent, activeSession := newSidecarLifecycleAgent(t, runtime, sessions, func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
		return agent.AgentSpec{}, errors.New("main agent spec should not be built for side ACP slash command")
	})

	_, err := runtimeAgent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/helper inspect the repo"}`),
		},
	}, &recordingPromptCallbacks{})
	if !errors.Is(err, promptErr) {
		t.Fatalf("Prompt(/helper) error = %v, want prompt failure", err)
	}
	if runtime.detachCount != 1 {
		t.Fatalf("detachCount = %d, want 1", runtime.detachCount)
	}
	if runtime.detach.ParticipantID != "participant-1" {
		t.Fatalf("detach request = %#v, want participant-1 rollback", runtime.detach)
	}
}

func newSidecarLifecycleAgent(
	t *testing.T,
	runtime *sidecarLifecycleRuntime,
	sessions session.Service,
	buildSpec func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error),
) (*runtimeacp.RuntimeAgent, acp.NewSessionResponse) {
	t.Helper()
	runtimeAgent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:        runtime,
		Sessions:       sessions,
		BuildAgentSpec: buildSpec,
		Commands:       sideACPCommandProvider{{Name: "helper", Description: "bounded helper"}},
		AppName:        "caelis",
		UserID:         "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := runtimeAgent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return runtimeAgent, activeSession
}

type sidecarLifecycleRuntime struct {
	sessions                 session.Service
	attachWithoutParticipant bool
	promptErr                error
	runCalled                bool
	attach                   agent.AttachParticipantRequest
	prompt                   agent.PromptParticipantRequest
	detach                   agent.DetachParticipantRequest
	detachCount              int
}

func (r *sidecarLifecycleRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.runCalled = true
	return agent.RunResult{Session: session.Session{SessionRef: req.SessionRef}, Handle: emptySidecarRun{}}, nil
}

func (r *sidecarLifecycleRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r *sidecarLifecycleRuntime) AttachParticipant(ctx context.Context, req agent.AttachParticipantRequest) (session.Session, error) {
	r.attach = req
	activeSession, err := r.sessions.Session(ctx, req.SessionRef)
	if err != nil {
		return session.Session{}, err
	}
	if r.attachWithoutParticipant {
		return activeSession, nil
	}
	role := req.Role
	if role == "" {
		role = session.ParticipantRoleSidecar
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "@" + strings.TrimSpace(req.Agent)
	}
	return r.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:        "participant-1",
			Kind:      session.ParticipantKindACP,
			Role:      role,
			AgentName: strings.TrimSpace(req.Agent),
			Label:     label,
			SessionID: "remote-helper",
			Source:    strings.TrimSpace(req.Source),
		},
	})
}

func (r *sidecarLifecycleRuntime) PromptParticipant(ctx context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
	r.prompt = req
	if r.promptErr != nil {
		return agent.RunResult{}, r.promptErr
	}
	activeSession, err := r.sessions.Session(ctx, req.SessionRef)
	if err != nil {
		return agent.RunResult{}, err
	}
	msg := model.NewTextMessage(model.RoleAssistant, "side acp output")
	event := &session.Event{
		SessionID:  activeSession.SessionID,
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &msg,
		Text:       msg.TextContent(),
		Protocol: &session.EventProtocol{
			UpdateType: acp.UpdateAgentMessage,
		},
	}
	return agent.RunResult{Session: activeSession, Handle: singleEventSidecarRun{event: event}}, nil
}

func (r *sidecarLifecycleRuntime) DetachParticipant(ctx context.Context, req agent.DetachParticipantRequest) (session.Session, error) {
	r.detach = req
	r.detachCount++
	return r.sessions.RemoveParticipant(ctx, session.RemoveParticipantRequest{
		SessionRef:    req.SessionRef,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
	})
}

func (r *sidecarLifecycleRuntime) HandoffController(context.Context, agent.HandoffControllerRequest) (session.Session, error) {
	return session.Session{}, fmt.Errorf("handoff not implemented")
}

type emptySidecarRun struct{}

func (emptySidecarRun) RunID() string { return "main-run-1" }
func (emptySidecarRun) Events() iter.Seq2[*session.Event, error] {
	return func(func(*session.Event, error) bool) {}
}
func (emptySidecarRun) Submit(agent.Submission) error { return nil }
func (emptySidecarRun) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (emptySidecarRun) Close() error { return nil }

type singleEventSidecarRun struct {
	event *session.Event
}

func (r singleEventSidecarRun) RunID() string { return "side-run-1" }
func (r singleEventSidecarRun) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		yield(r.event, nil)
	}
}
func (r singleEventSidecarRun) Submit(agent.Submission) error { return nil }
func (r singleEventSidecarRun) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (r singleEventSidecarRun) Close() error { return nil }
