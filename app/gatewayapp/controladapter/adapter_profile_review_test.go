package controladapter

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
)

func TestAdapterStartReviewSubagentUsesHiddenReviewerProfile(t *testing.T) {
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
		Gateway: GatewayRuntimeDeps{ServiceFn: func() GatewayService { return gw }},
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return session.CloneSession(gw.session), nil
			},
		},
		AgentProfile: AgentProfileRuntimeDeps{
			StatusFn: func(context.Context) (AgentProfileStatusSnapshot, error) {
				return AgentProfileStatusSnapshot{Profiles: []AgentProfileSnapshot{{
					ID:      "reviewer",
					Enabled: true,
					Target:  "built_in",
					Status:  "ok",
				}}}, nil
			},
		},
	}, activeSession.SessionID, "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	imageRaw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeSession.CWD, "review.png"), imageRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	turn, err := driver.StartReviewSubagent(ctx, "  inspect the image  ", []Attachment{{Name: "review.png", Offset: len([]rune("inspect the "))}})
	if err != nil {
		t.Fatalf("StartReviewSubagent() error = %v", err)
	}
	if turn == nil {
		t.Fatal("StartReviewSubagent() turn = nil, want participant stream")
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
	for _, want := range []string{"Review the current workspace changes", "staged, unstaged, and untracked", "Additional review instructions", "inspect the image"} {
		if !strings.Contains(gw.promptReqs[0].Input, want) {
			t.Fatalf("review prompt = %q, want %q", gw.promptReqs[0].Input, want)
		}
	}
	if parts := gw.promptReqs[0].ContentParts; len(parts) != 3 ||
		parts[0].Type != model.ContentPartText || !strings.HasSuffix(parts[0].Text, "inspect the ") ||
		parts[1].Type != model.ContentPartImage || parts[1].FileName != "review.png" ||
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

func TestAdapterStartReviewSubagentExternalACPReviewerPrompts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantPrompt string
	}{
		{
			name:       "user prompt",
			input:      "  focus on auth  ",
			wantPrompt: "Review request:\nReview the current workspace changes, including staged, unstaged, and untracked files.\n\nUser review instructions:\nfocus on auth",
		},
		{
			name:       "empty review request",
			input:      "   ",
			wantPrompt: "Review request:\nReview the current workspace changes, including staged, unstaged, and untracked files.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			driver, gw := newReviewProfileAdapterForTest(t, ctx, "review-profile-acp-"+strings.ReplaceAll(tt.name, " ", "-"), AgentProfileStatusSnapshot{
				Profiles: []AgentProfileSnapshot{{
					ID:       "reviewer",
					Enabled:  true,
					Target:   "acp",
					ACPAgent: "codex",
					Status:   "ok",
				}},
			})

			turn, err := driver.StartReviewSubagent(ctx, tt.input, nil)
			if err != nil {
				t.Fatalf("StartReviewSubagent() error = %v", err)
			}
			if turn == nil {
				t.Fatal("StartReviewSubagent() turn = nil, want participant stream")
			}
			if len(gw.attachReqs) != 1 || gw.attachReqs[0].Agent != "reviewer" || gw.attachReqs[0].Label == "" || strings.EqualFold(strings.TrimPrefix(gw.attachReqs[0].Label, "@"), "reviewer") {
				t.Fatalf("AttachParticipant requests = %#v, want reviewer profile attached with external ACP label", gw.attachReqs)
			}
			if len(gw.promptReqs) != 1 {
				t.Fatalf("PromptParticipant requests = %#v, want one reviewer prompt", gw.promptReqs)
			}
			if prompt := gw.promptReqs[0].Input; prompt != tt.wantPrompt {
				t.Fatalf("external review prompt = %q, want %q", prompt, tt.wantPrompt)
			}
			drainReviewProfileTurnEvents(t, turn)
			if len(gw.detachReqs) != 1 || gw.detachReqs[0].ParticipantID != "side-reviewer" {
				t.Fatalf("DetachParticipant requests = %#v, want review sidecar completion detach", gw.detachReqs)
			}
		})
	}
}

func TestAdapterStartReviewSubagentAllowsWarningReviewer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName:      "caelis",
			UserID:       "review-profile-test",
			SessionID:    "review-profile-warning",
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
		Gateway: GatewayRuntimeDeps{ServiceFn: func() GatewayService { return gw }},
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return session.CloneSession(gw.session), nil
			},
		},
		AgentProfile: AgentProfileRuntimeDeps{
			StatusFn: func(context.Context) (AgentProfileStatusSnapshot, error) {
				return AgentProfileStatusSnapshot{Profiles: []AgentProfileSnapshot{{
					ID:      "reviewer",
					Enabled: true,
					Target:  "built_in",
					Status:  "warning",
					Warning: "model reasoning level is unavailable",
				}}}, nil
			},
		},
	}, activeSession.SessionID, "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}

	turn, err := driver.StartReviewSubagent(ctx, "inspect", nil)
	if err != nil {
		t.Fatalf("StartReviewSubagent(warning) error = %v, want runnable reviewer", err)
	}
	if turn == nil {
		t.Fatal("StartReviewSubagent(warning) turn = nil, want participant stream")
	}
	if len(gw.attachReqs) != 1 || len(gw.promptReqs) != 1 {
		t.Fatalf("gateway requests = attach %#v prompt %#v, want reviewer start", gw.attachReqs, gw.promptReqs)
	}
	drainReviewProfileTurnEvents(t, turn)
	if len(gw.detachReqs) != 1 || gw.detachReqs[0].ParticipantID != "side-reviewer" {
		t.Fatalf("DetachParticipant requests = %#v, want review sidecar completion detach", gw.detachReqs)
	}
}

func TestAdapterStartReviewSubagentRejectsUnavailableReviewer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   AgentProfileStatusSnapshot
		wantText string
	}{
		{
			name:     "missing",
			status:   AgentProfileStatusSnapshot{},
			wantText: "unavailable",
		},
		{
			name: "disabled",
			status: AgentProfileStatusSnapshot{Profiles: []AgentProfileSnapshot{{
				ID:      "reviewer",
				Enabled: false,
				Status:  "ok",
			}}},
			wantText: "disabled",
		},
		{
			name: "stale",
			status: AgentProfileStatusSnapshot{Profiles: []AgentProfileSnapshot{{
				ID:      "reviewer",
				Enabled: true,
				Status:  "stale",
				Warning: "model alias is missing",
			}}},
			wantText: "model alias is missing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			activeSession := session.Session{
				SessionRef: session.SessionRef{
					AppName:      "caelis",
					UserID:       "review-profile-test",
					SessionID:    "review-profile-" + tt.name,
					WorkspaceKey: "ws",
				},
				CWD: t.TempDir(),
			}
			gw := &reviewProfileGatewayService{session: activeSession}
			driver, err := NewAdapter(ctx, &RuntimeStack{
				Gateway: GatewayRuntimeDeps{ServiceFn: func() GatewayService { return gw }},
				Session: SessionRuntimeDeps{
					Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
					StartFn: func(context.Context, string, string) (session.Session, error) {
						return session.CloneSession(gw.session), nil
					},
				},
				AgentProfile: AgentProfileRuntimeDeps{
					StatusFn: func(context.Context) (AgentProfileStatusSnapshot, error) {
						return tt.status, nil
					},
				},
			}, activeSession.SessionID, "surface", "ollama/llama3")
			if err != nil {
				t.Fatalf("NewAdapter() error = %v", err)
			}

			_, err = driver.StartReviewSubagent(ctx, "inspect", nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("StartReviewSubagent() error = %v, want %q", err, tt.wantText)
			}
			if len(gw.attachReqs) != 0 || len(gw.promptReqs) != 0 {
				t.Fatalf("gateway requests = attach %#v prompt %#v, want no participant start", gw.attachReqs, gw.promptReqs)
			}
		})
	}
}

func newReviewProfileAdapterForTest(t *testing.T, ctx context.Context, sessionID string, status AgentProfileStatusSnapshot) (*Adapter, *reviewProfileGatewayService) {
	t.Helper()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName:      "caelis",
			UserID:       "review-profile-test",
			SessionID:    sessionID,
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
		Gateway: GatewayRuntimeDeps{ServiceFn: func() GatewayService { return gw }},
		Session: SessionRuntimeDeps{
			Workspace: session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return session.CloneSession(gw.session), nil
			},
		},
		AgentProfile: AgentProfileRuntimeDeps{
			StatusFn: func(context.Context) (AgentProfileStatusSnapshot, error) {
				return status, nil
			},
		},
	}, activeSession.SessionID, "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	return driver, gw
}

type reviewProfileGatewayService struct {
	activeSubmitGatewayService
	session    session.Session
	handle     gateway.TurnHandle
	attachReqs []gateway.AttachParticipantRequest
	promptReqs []gateway.PromptParticipantRequest
	detachReqs []gateway.DetachParticipantRequest
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
	ref    session.SessionRef
	events chan gateway.EventEnvelope
}

func reviewProfileTurnHandle(ref session.SessionRef) *reviewProfileHandle {
	events := make(chan gateway.EventEnvelope, 1)
	events <- gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindAssistantMessage,
		SessionRef: ref,
		Origin: &gateway.EventOrigin{
			Scope:   gateway.EventScopeParticipant,
			ScopeID: "side-reviewer",
			Actor:   "@reviewer",
		},
		Narrative: &gateway.NarrativePayload{
			Role:  gateway.NarrativeRoleAssistant,
			Actor: "@reviewer",
			Text:  "findings",
			Final: true,
			Scope: gateway.EventScopeParticipant,
		},
	}}
	close(events)
	return &reviewProfileHandle{ref: ref, events: events}
}

func (h *reviewProfileHandle) HandleID() string { return "review-handle" }
func (h *reviewProfileHandle) RunID() string    { return "review-run" }
func (h *reviewProfileHandle) TurnID() string   { return "review-turn" }
func (h *reviewProfileHandle) SessionRef() session.SessionRef {
	return h.ref
}
func (h *reviewProfileHandle) CreatedAt() time.Time { return time.Time{} }
func (h *reviewProfileHandle) Events() <-chan gateway.EventEnvelope {
	return h.events
}
func (h *reviewProfileHandle) EventsAfter(string) ([]gateway.EventEnvelope, string, error) {
	return nil, "", nil
}
func (h *reviewProfileHandle) Submit(context.Context, gateway.SubmitRequest) error {
	return nil
}
func (h *reviewProfileHandle) Cancel() agent.CancelResult {
	return agent.CancelResult{}
}
func (h *reviewProfileHandle) Close() error { return nil }
