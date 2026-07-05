package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestTransientParticipantFinishHookDetachesWithoutOuterConsumer(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	detached := make(chan struct{})
	handle.onFinish(func() { close(detached) })
	for i := 0; i < 64; i++ {
		handle.publishACP(eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "event"}, "")
	}
	handle.finish()

	select {
	case <-detached:
	case <-time.After(2 * time.Second):
		t.Fatal("transient participant finish hook did not detach")
	}
}

func TestTransientParticipantFinishHookCanPublishError(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	handle.onFinish(func() {
		handle.publishError(errors.New("detach failed"))
	})
	handle.finish()

	var sawDetachFailure bool
	for env := range handle.ACPEvents() {
		if strings.Contains(env.Error, "detach failed") {
			sawDetachFailure = true
		}
	}
	if !sawDetachFailure {
		t.Fatal("finish hook error was not published before the event stream closed")
	}
}

func TestStartParticipantTransientDetachesWhenRuntimeReturnsNoHandle(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	attachedSession := activeSession
	attachedSession.Participants = []session.ParticipantBinding{{
		ID:        "participant-1",
		Kind:      session.ParticipantKindACP,
		Role:      session.ParticipantRoleSidecar,
		AgentName: "reviewer",
		Label:     "@reviewer",
	}}
	rt := &controlPlaneRuntime{
		session:    activeSession,
		attachResp: attachedSession,
		detachResp: activeSession,
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-agent",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	result, err := gw.StartParticipant(context.Background(), StartParticipantRequest{
		BindingKey:   "surface-agent",
		Agent:        "reviewer",
		Label:        "@reviewer",
		Input:        "inspect",
		Source:       "slash_review",
		Lifecycle:    ParticipantLifecycleTransient,
		DetachSource: "side_agent_complete",
	})
	if err != nil {
		t.Fatalf("StartParticipant(transient) error = %v", err)
	}
	if result.Handle == nil {
		t.Fatal("StartParticipant(transient) handle = nil, want kernel wrapper handle")
	}
	for range result.Handle.ACPEvents() {
	}
	if rt.promptReq.ParticipantID != "participant-1" || rt.promptReq.Input != "inspect" {
		t.Fatalf("promptReq = %+v, want participant prompt", rt.promptReq)
	}
	detachReq := waitForTransientParticipantDetach(t, rt)
	if detachReq.ParticipantID != "participant-1" || detachReq.Source != "side_agent_complete" {
		t.Fatalf("detachReq = %+v, want completion detach", detachReq)
	}
}

func TestStartParticipantTransientPublishesDetachFailure(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	attachedSession := activeSession
	attachedSession.Participants = []session.ParticipantBinding{{
		ID:        "participant-1",
		Kind:      session.ParticipantKindACP,
		Role:      session.ParticipantRoleSidecar,
		AgentName: "reviewer",
		Label:     "@reviewer",
	}}
	rt := &controlPlaneRuntime{
		session:    activeSession,
		attachResp: attachedSession,
		detachErr:  errors.New("store unavailable"),
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-agent",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	result, err := gw.StartParticipant(context.Background(), StartParticipantRequest{
		BindingKey:   "surface-agent",
		Agent:        "reviewer",
		Label:        "@reviewer",
		Input:        "inspect",
		Source:       "slash_review",
		Lifecycle:    ParticipantLifecycleTransient,
		DetachSource: "side_agent_complete",
	})
	if err != nil {
		t.Fatalf("StartParticipant(transient) error = %v", err)
	}
	var sawDetachFailure bool
	for env := range result.Handle.ACPEvents() {
		if strings.Contains(env.Error, "transient participant detach failed") &&
			strings.Contains(env.Error, "store unavailable") {
			sawDetachFailure = true
		}
	}
	if !sawDetachFailure {
		t.Fatal("StartParticipant(transient) did not publish detach failure")
	}
	if rt.detachReq.ParticipantID != "participant-1" || rt.detachReq.Source != "side_agent_complete" {
		t.Fatalf("detachReq = %+v, want completion detach", rt.detachReq)
	}
}

func TestStartParticipantTransientRollsBackWhenParticipantIDResolutionFails(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Participants: []session.ParticipantBinding{{
			ID:        "existing",
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			AgentName: "existing",
			Label:     "@existing",
		}},
	}
	attachedSession := activeSession
	attachedSession.Participants = append(attachedSession.Participants, session.ParticipantBinding{
		ID:        "participant-1",
		Kind:      session.ParticipantKindACP,
		Role:      session.ParticipantRoleSidecar,
		AgentName: "unexpected-agent",
		Label:     "@reviewer",
	})
	rt := &controlPlaneRuntime{
		session:    activeSession,
		attachResp: attachedSession,
		detachResp: activeSession,
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-agent",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	_, err = gw.StartParticipant(context.Background(), StartParticipantRequest{
		BindingKey: "surface-agent",
		Agent:      "reviewer",
		Label:      "@reviewer",
		Input:      "inspect",
		Source:     "slash_review",
		Lifecycle:  ParticipantLifecycleTransient,
	})
	if err == nil || !strings.Contains(err.Error(), "participant was not attached") {
		t.Fatalf("StartParticipant(transient) error = %v, want participant resolution failure", err)
	}
	if rt.detachReq.ParticipantID != "participant-1" || rt.detachReq.Source != "side_agent_prompt_rollback" {
		t.Fatalf("detachReq = %+v, want rollback of newly attached participant", rt.detachReq)
	}
}

func TestStartParticipantTransientDetachesWhenRuntimePromptErrors(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	attachedSession := activeSession
	attachedSession.Participants = []session.ParticipantBinding{{
		ID:        "participant-1",
		Kind:      session.ParticipantKindACP,
		Role:      session.ParticipantRoleSidecar,
		AgentName: "copilot",
		Label:     "@copilot",
	}}
	rt := &controlPlaneRuntime{
		session:    activeSession,
		attachResp: attachedSession,
		promptErr:  errors.New("active run conflict"),
		detachResp: activeSession,
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-agent",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	result, err := gw.StartParticipant(context.Background(), StartParticipantRequest{
		BindingKey: "surface-agent",
		Agent:      "copilot",
		Label:      "@copilot",
		Input:      "inspect",
		Source:     "slash_copilot",
		Lifecycle:  ParticipantLifecycleTransient,
	})
	if err != nil {
		t.Fatalf("StartParticipant(transient) error = %v", err)
	}
	if result.Handle == nil {
		t.Fatal("StartParticipant(transient) handle = nil, want kernel wrapper handle")
	}
	var sawPromptError bool
	for env := range result.Handle.ACPEvents() {
		if strings.Contains(env.Error, "active run conflict") {
			sawPromptError = true
		}
	}
	if !sawPromptError {
		t.Fatal("StartParticipant(transient) events missing runtime prompt error")
	}
	detachReq := waitForTransientParticipantDetach(t, rt)
	if detachReq.ParticipantID != "participant-1" || detachReq.Source != "side_agent_prompt_failed" {
		t.Fatalf("detachReq = %+v, want failed prompt detach after async prompt error", detachReq)
	}
}

func waitForTransientParticipantDetach(t *testing.T, rt *controlPlaneRuntime) agent.DetachParticipantRequest {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.TrimSpace(rt.detachReq.ParticipantID) != "" {
			return rt.detachReq
		}
		select {
		case <-deadline:
			t.Fatalf("detachReq = %+v, want participant detach", rt.detachReq)
		case <-tick.C:
		}
	}
}
