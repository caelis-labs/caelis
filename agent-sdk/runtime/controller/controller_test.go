package controller

import (
	"errors"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestCancelResultCancelled(t *testing.T) {
	t.Parallel()

	if !(CancelResult{Status: CancelStatusCancelled}).Cancelled() {
		t.Fatal("cancelled result should report Cancelled() = true")
	}
	if (CancelResult{Status: CancelStatusAlreadyCancelled}).Cancelled() {
		t.Fatal("already-cancelled result should report Cancelled() = false")
	}
	if (CancelResult{Err: errors.New("boom")}).Cancelled() {
		t.Fatal("error-only result should report Cancelled() = false")
	}
}

func TestNormalizeTurnRequestTrimsAndClones(t *testing.T) {
	t.Parallel()

	part := model.ContentPart{Type: model.ContentPartText, Text: "hello"}
	in := TurnRequest{
		SessionRef: session.SessionRef{SessionID: "  session-1  "},
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: "session-1"},
			Metadata: map[string]any{
				"mode": "agent",
			},
		},
		TurnID:       " turn-1 ",
		Input:        "  prompt  ",
		ContentParts: []model.ContentPart{part},
		Context: agent.ContextTransfer{Turns: []agent.ContextTurn{{
			Executor: session.ActorRef{Name: " helper "}, UserMessages: []string{" prior "}, AssistantSummary: " done ",
		}}},
		ContextSyncSeq: 3,
		Mode:           " plan ",
	}
	got := NormalizeTurnRequest(in)

	if got.SessionRef.SessionID != "session-1" {
		t.Fatalf("session ref = %q, want session-1", got.SessionRef.SessionID)
	}
	if got.TurnID != "turn-1" || got.Input != "prompt" || got.Mode != "plan" || len(got.Context.Turns) != 1 || got.Context.Turns[0].Executor.Name != "helper" {
		t.Fatalf("trimmed fields = %+v", got)
	}
	if got.ContextSyncSeq != 3 {
		t.Fatalf("context sync seq = %d, want 3", got.ContextSyncSeq)
	}
	if len(got.ContentParts) != 1 || got.ContentParts[0].Text != "hello" {
		t.Fatalf("content parts = %+v", got.ContentParts)
	}
	in.ContentParts[0].Text = "mutated"
	if got.ContentParts[0].Text != "hello" {
		t.Fatal("NormalizeTurnRequest should clone content parts")
	}
	in.Session.Metadata["mode"] = "mutated"
	if got.Session.Metadata["mode"] != "agent" {
		t.Fatal("NormalizeTurnRequest should clone session metadata")
	}
}

func TestNormalizeParticipantPromptRequestTrimsFields(t *testing.T) {
	t.Parallel()

	got := NormalizeParticipantPromptRequest(ParticipantPromptRequest{
		SessionRef:    session.SessionRef{SessionID: "  sid  "},
		TurnID:        " turn ",
		ParticipantID: " participant ",
		Input:         " input ",
		Mode:          " mode ",
		Stream:        true,
	})
	if got.SessionRef.SessionID != "sid" || got.TurnID != "turn" || got.ParticipantID != "participant" || got.Input != "input" || got.Mode != "mode" {
		t.Fatalf("normalized participant prompt = %+v", got)
	}
	if !got.Stream {
		t.Fatal("stream flag should be preserved")
	}
}

func TestNormalizeAttachDetachHandoffRequests(t *testing.T) {
	t.Parallel()

	attach := NormalizeAttachRequest(AttachRequest{
		SessionRef:      session.SessionRef{SessionID: " sid "},
		Agent:           " agent ",
		Source:          " source ",
		Label:           " label ",
		ReasoningEffort: " xhigh ",
	})
	if attach.SessionRef.SessionID != "sid" || attach.Agent != "agent" || attach.Source != "source" || attach.Label != "label" || attach.ReasoningEffort != "xhigh" {
		t.Fatalf("normalized attach = %+v", attach)
	}

	detach := NormalizeDetachRequest(DetachRequest{
		SessionRef:    session.SessionRef{SessionID: " sid "},
		ParticipantID: " pid ",
		Source:        " source ",
	})
	if detach.SessionRef.SessionID != "sid" || detach.ParticipantID != "pid" || detach.Source != "source" {
		t.Fatalf("normalized detach = %+v", detach)
	}

	handoff := NormalizeHandoffRequest(HandoffRequest{
		SessionRef:     session.SessionRef{SessionID: " sid "},
		Agent:          " agent ",
		Source:         " source ",
		Reason:         " reason ",
		Context:        agent.ContextTransfer{Summary: " summary "},
		ContextSyncSeq: 9,
	})
	if handoff.SessionRef.SessionID != "sid" || handoff.Agent != "agent" || handoff.Reason != "reason" || handoff.Context.Summary != "summary" || handoff.ContextSyncSeq != 9 {
		t.Fatalf("normalized handoff = %+v", handoff)
	}
}
