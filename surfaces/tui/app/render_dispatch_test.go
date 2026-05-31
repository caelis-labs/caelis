package tuiapp

import (
	"testing"
)

func TestRenderEventPolicyForTranscriptToolUsesStructuredToolLane(t *testing.T) {
	policy, ok := renderEventPolicyFor(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "call-1",
		ToolName:   "READ",
		ToolStatus: transcriptToolStatusRunning,
	}}})
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
	policy, ok := renderEventPolicyFor(TranscriptEventsMsg{Events: []TranscriptEvent{{
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
