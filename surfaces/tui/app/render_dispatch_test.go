package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestRenderEventPolicyForGatewayEnvelopeUsesStructuredToolLane(t *testing.T) {
	policy, ok := renderEventPolicyFor(gatewayEventMsg(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind: kernel.EventKindToolCall,
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				Status:   "running",
				Scope:    kernel.EventScopeMain,
			},
		}}))

	if !ok {
		t.Fatal("renderEventPolicyFor() = not ok, want ok")
	}
	if policy.lane != renderLaneToolStream {
		t.Fatalf("renderEventPolicyFor() lane = %q, want %q", policy.lane, renderLaneToolStream)
	}
	if !policy.flushLogChunks {
		t.Fatal("renderEventPolicyFor() did not flush pending log chunks before structured tool events")
	}
}

func TestRenderEventPolicyForACPEnvelopeUsesStructuredToolLane(t *testing.T) {
	status := schema.ToolStatusInProgress
	kind := "read"
	policy, ok := renderEventPolicyFor(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        &status,
		},
	})
	if !ok {
		t.Fatal("renderEventPolicyFor() = not ok, want ok")
	}
	if policy.lane != renderLaneToolStream {
		t.Fatalf("renderEventPolicyFor() lane = %q, want %q", policy.lane, renderLaneToolStream)
	}
	if !policy.flushLogChunks {
		t.Fatal("renderEventPolicyFor() did not flush pending log chunks before ACP tool events")
	}
}

func TestRenderEventPolicyKeepsSmoothingForNonFinalNarrative(t *testing.T) {
	policy, ok := renderEventPolicyFor(gatewayEventMsg(kernel.EventEnvelope{
		Event: kernel.Event{
			Kind: kernel.EventKindAssistantMessage,
			Narrative: &kernel.NarrativePayload{
				Role: kernel.NarrativeRoleAssistant,
				Text: "chunk",
			},
		}}))

	if !ok {
		t.Fatal("renderEventPolicyFor() = not ok, want ok")
	}
	if policy.flushSmoothing {
		t.Fatal("non-final assistant gateway chunk should not flush pending smoothing")
	}

	policy, ok = renderEventPolicyFor(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeAssistant,
		Text:          "chunk",
	}}})
	if !ok {
		t.Fatal("renderEventPolicyFor(transcript) = not ok, want ok")
	}
	if policy.flushSmoothing {
		t.Fatal("non-final transcript narrative should not flush pending smoothing")
	}

	policy, ok = renderEventPolicyFor(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeAssistant,
		Text:          "done",
		Final:         true,
	}}})
	if !ok {
		t.Fatal("renderEventPolicyFor(final transcript) = not ok, want ok")
	}
	if !policy.flushSmoothing {
		t.Fatal("final transcript narrative should flush pending smoothing")
	}
}

func TestModelUpdateRendersACPEnvelopeWithoutKernelEnvelope(t *testing.T) {
	env := eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "root-session",
		Scope:     eventstream.ScopeMain,
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "hello from acp"},
		},
	}
	projected := ProjectACPEventToTranscriptEvents(env)
	if len(projected) != 1 || projected[0].Text != "hello from acp" {
		t.Fatalf("ProjectACPEventToTranscriptEvents() = %#v, want ACP message transcript", projected)
	}
	model := newGatewayEventTestModel()
	updated, _ := model.Update(env)
	model = updated.(*Model)
	if len(model.doc.Blocks()) == 0 {
		t.Fatal("doc has no transcript blocks after ACP envelope update")
	}
	rows := model.doc.Blocks()[0].Render(BlockRenderContext{Width: 80, TermWidth: 80, Theme: model.theme})
	if joined := strings.Join(renderedPlainRows(rows), "\n"); !strings.Contains(joined, "hello from acp") {
		t.Fatalf("rendered rows = %q, want ACP message text", joined)
	}
}
