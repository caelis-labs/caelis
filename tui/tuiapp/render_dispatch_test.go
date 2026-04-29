package tuiapp

import (
	"testing"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
)

func TestRenderEventPolicyForGatewayEnvelopeUsesStructuredToolLane(t *testing.T) {
	policy, ok := renderEventPolicyFor(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind: appgateway.EventKindToolCall,
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				Status:   "running",
				Scope:    appgateway.EventScopeMain,
			},
		},
	})
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

func TestRenderEventPolicyKeepsSmoothingForNonFinalNarrative(t *testing.T) {
	policy, ok := renderEventPolicyFor(appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind: appgateway.EventKindAssistantMessage,
			Narrative: &appgateway.NarrativePayload{
				Role: appgateway.NarrativeRoleAssistant,
				Text: "chunk",
			},
		},
	})
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
