package controladapter

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestAdapterStartReviewUsesHiddenReviewerProfile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName:      "caelis",
			UserID:       "review-profile-test",
			SessionID:    "review-profile-session",
			WorkspaceKey: "ws",
		},
		CWD:        t.TempDir(),
		Controller: session.ControllerBinding{Kind: session.ControllerKindKernel},
	}
	gw := &reviewProfileGatewayService{
		session: activeSession,
		handle:  reviewProfileTurnHandle(activeSession.SessionRef),
	}
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(gw),
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return session.CloneSession(gw.session), nil
			},
		},
	}, activeSession.SessionID, "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	imageData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	turn, err := driver.StartReview(ctx, "  inspect the image  ", []Attachment{{
		Name:     "review.png",
		Offset:   len([]rune("inspect the ")),
		MimeType: "image/png",
		Data:     imageData,
	}})
	if err != nil {
		t.Fatalf("StartReview() error = %v", err)
	}
	if turn == nil {
		t.Fatal("StartReview() turn = nil, want participant stream")
	}
	if len(gw.attachReqs) != 1 || gw.attachReqs[0].Agent != "reviewer" || gw.attachReqs[0].Label == "" || gw.attachReqs[0].Source != "slash_review" {
		t.Fatalf("AttachParticipant requests = %#v, want hidden reviewer slash_review attach", gw.attachReqs)
	}
	if len(gw.promptReqs) != 1 || gw.promptReqs[0].ParticipantID != "side-reviewer" || gw.promptReqs[0].Source != "slash_review" {
		t.Fatalf("PromptParticipant requests = %#v, want reviewer participant prompt", gw.promptReqs)
	}
	if got := gw.promptReqs[0].DisplayInput; got != "inspect the [image #1] image" {
		t.Fatalf("review DisplayInput = %q, want image marker", got)
	}
	for _, want := range []string{"Strictly review the current workspace changes", "staged, unstaged, and untracked", "$review skill", "Additional review instructions", "inspect the image"} {
		if !strings.Contains(gw.promptReqs[0].Input, want) {
			t.Fatalf("review prompt = %q, want %q", gw.promptReqs[0].Input, want)
		}
	}
	if parts := gw.promptReqs[0].ContentParts; len(parts) != 3 ||
		parts[0].Type != model.ContentPartText || !strings.HasSuffix(parts[0].Text, "inspect the ") ||
		parts[1].Type != model.ContentPartImage || parts[1].FileName != "review.png" || parts[1].MimeType != "image/png" || parts[1].Data != imageData ||
		parts[2].Type != model.ContentPartText || parts[2].Text != "image" {
		t.Fatalf("review prompt content parts = %#v, want prefixed text/image/text", parts)
	}
	events := drainReviewProfileTurnEvents(t, turn)
	if len(events) == 0 {
		t.Fatal("reviewer turn emitted no event")
	}
	if events[0].Scope != eventstream.ScopeParticipant {
		t.Fatalf("event scope = %#v, want participant stream", events[0].Scope)
	}
	if len(gw.detachReqs) != 1 || gw.detachReqs[0].ParticipantID != "side-reviewer" || gw.detachReqs[0].Source != "side_agent_complete" {
		t.Fatalf("DetachParticipant requests = %#v, want review sidecar completion detach", gw.detachReqs)
	}
	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
	if len(status.Participants) != 0 {
		t.Fatalf("AgentStatus().Participants = %#v, want completed review sidecar hidden", status.Participants)
	}
}

type reviewProfileGatewayService struct {
	activeSubmitGatewayService
	session    session.Session
	handle     gateway.TurnHandle
	attachReqs []gateway.AttachParticipantRequest
	promptReqs []gateway.PromptParticipantRequest
	detachReqs []gateway.DetachParticipantRequest
}

func (*reviewProfileGatewayService) HandoffController(context.Context, gateway.HandoffControllerRequest) (session.Session, error) {
	return session.Session{}, nil
}

func (g *reviewProfileGatewayService) ControlPlaneState(context.Context, gateway.ControlPlaneStateRequest) (gateway.ControlPlaneState, error) {
	participants := make([]gateway.ParticipantState, 0, len(g.session.Participants))
	for _, participant := range g.session.Participants {
		participants = append(participants, gateway.ParticipantState{
			ID:        participant.ID,
			Kind:      participant.Kind,
			Role:      participant.Role,
			AgentName: participant.AgentName,
			Label:     participant.Label,
			SessionID: participant.SessionID,
			Source:    participant.Source,
		})
	}
	return gateway.ControlPlaneState{
		SessionRef:   g.session.SessionRef,
		Controller:   gateway.ControllerState{Kind: g.session.Controller.Kind},
		Participants: participants,
	}, nil
}

func (g *reviewProfileGatewayService) AttachParticipant(_ context.Context, req gateway.AttachParticipantRequest) (session.Session, error) {
	g.attachReqs = append(g.attachReqs, req)
	g.session.Participants = append(g.session.Participants, session.ParticipantBinding{
		ID:        "side-reviewer",
		Kind:      session.ParticipantKindACP,
		Role:      req.Role,
		AgentName: req.Agent,
		Label:     req.Label,
		SessionID: "remote-reviewer",
		Source:    req.Source,
	})
	return session.CloneSession(g.session), nil
}

func (g *reviewProfileGatewayService) PromptParticipant(_ context.Context, req gateway.PromptParticipantRequest) (gateway.BeginTurnResult, error) {
	g.promptReqs = append(g.promptReqs, req)
	return gateway.BeginTurnResult{Session: session.CloneSession(g.session), Handle: g.handle}, nil
}

func (g *reviewProfileGatewayService) StartParticipant(ctx context.Context, req gateway.StartParticipantRequest) (gateway.BeginTurnResult, error) {
	updated, err := g.AttachParticipant(ctx, gateway.AttachParticipantRequest{
		SessionRef: req.SessionRef,
		BindingKey: req.BindingKey,
		Agent:      req.Agent,
		Role:       req.Role,
		Source:     req.Source,
		Label:      req.Label,
	})
	if err != nil {
		return gateway.BeginTurnResult{}, err
	}
	result, err := g.PromptParticipant(ctx, gateway.PromptParticipantRequest{
		SessionRef:    updated.SessionRef,
		BindingKey:    req.BindingKey,
		ParticipantID: "side-reviewer",
		Input:         req.Input,
		DisplayInput:  req.DisplayInput,
		DisplayTitle:  req.DisplayTitle,
		ContentParts:  req.ContentParts,
		Source:        req.Source,
	})
	if err != nil {
		_, _ = g.DetachParticipant(ctx, gateway.DetachParticipantRequest{
			SessionRef:    updated.SessionRef,
			BindingKey:    req.BindingKey,
			ParticipantID: "side-reviewer",
			Source:        "side_agent_prompt_rollback",
		})
		return gateway.BeginTurnResult{}, err
	}
	if req.Lifecycle == gateway.ParticipantLifecycleTransient {
		if result.Handle == nil {
			_, _ = g.DetachParticipant(ctx, gateway.DetachParticipantRequest{
				SessionRef:    updated.SessionRef,
				BindingKey:    req.BindingKey,
				ParticipantID: "side-reviewer",
				Source:        firstNonEmpty(req.DetachSource, "side_agent_complete"),
			})
		} else if handle, ok := result.Handle.(*reviewProfileHandle); ok {
			handle.onFinish(func() {
				_, _ = g.DetachParticipant(ctx, gateway.DetachParticipantRequest{
					SessionRef:    updated.SessionRef,
					BindingKey:    req.BindingKey,
					ParticipantID: "side-reviewer",
					Source:        firstNonEmpty(req.DetachSource, "side_agent_complete"),
				})
			})
		}
	}
	return result, nil
}

func (g *reviewProfileGatewayService) DetachParticipant(_ context.Context, req gateway.DetachParticipantRequest) (session.Session, error) {
	g.detachReqs = append(g.detachReqs, req)
	kept := g.session.Participants[:0]
	for _, participant := range g.session.Participants {
		if participant.ID == req.ParticipantID {
			continue
		}
		kept = append(kept, participant)
	}
	g.session.Participants = kept
	return session.CloneSession(g.session), nil
}

func drainReviewProfileTurnEvents(t *testing.T, turn Turn) []eventstream.Envelope {
	t.Helper()
	var out []eventstream.Envelope
	for env := range turn.Events() {
		out = append(out, env)
	}
	return out
}

type reviewProfileHandle struct {
	ref         session.SessionRef
	acpEvents   chan eventstream.Envelope
	mu          sync.Mutex
	finished    bool
	finishHooks []func()
}

func reviewProfileTurnHandle(ref session.SessionRef) *reviewProfileHandle {
	handle := &reviewProfileHandle{ref: ref, acpEvents: make(chan eventstream.Envelope, 1)}
	go func() {
		defer close(handle.acpEvents)
		defer handle.finish()
		handle.acpEvents <- eventstream.Envelope{
			Kind:    eventstream.KindSessionUpdate,
			Scope:   eventstream.ScopeParticipant,
			ScopeID: "side-reviewer",
			Actor:   "@reviewer",
			Final:   true,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "findings"},
			},
		}
	}()
	return handle
}

func (h *reviewProfileHandle) HandleID() string { return "review-handle" }
func (h *reviewProfileHandle) RunID() string    { return "review-run" }
func (h *reviewProfileHandle) TurnID() string   { return "review-turn" }
func (h *reviewProfileHandle) SessionRef() session.SessionRef {
	return h.ref
}
func (h *reviewProfileHandle) CreatedAt() time.Time { return time.Time{} }
func (h *reviewProfileHandle) ACPEvents() <-chan eventstream.Envelope {
	return h.acpEvents
}
func (h *reviewProfileHandle) Submit(context.Context, gateway.SubmitRequest) error {
	return nil
}
func (h *reviewProfileHandle) Cancel() agent.CancelResult {
	return agent.CancelResult{}
}
func (h *reviewProfileHandle) Close() error { return nil }

func (h *reviewProfileHandle) onFinish(fn func()) {
	if fn == nil {
		return
	}
	h.mu.Lock()
	if h.finished {
		h.mu.Unlock()
		fn()
		return
	}
	h.finishHooks = append(h.finishHooks, fn)
	h.mu.Unlock()
}

func (h *reviewProfileHandle) finish() {
	h.mu.Lock()
	if h.finished {
		h.mu.Unlock()
		return
	}
	h.finished = true
	hooks := append([]func(){}, h.finishHooks...)
	h.finishHooks = nil
	h.mu.Unlock()
	for _, hook := range hooks {
		hook()
	}
}
