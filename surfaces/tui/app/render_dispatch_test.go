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

func TestTranscriptUserEchoDoesNotDuplicateSubmittedUserBlock(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.submitLine("介绍一下你自己")
	model = updated.(*Model)

	updated, _ = model.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeUser,
		Text:          "介绍一下你自己",
		Final:         true,
	}, {
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeAssistant,
		Text:          "你好",
		Final:         true,
	}}})
	model = updated.(*Model)

	count := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && user.Raw == "介绍一下你自己" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("submitted user blocks = %d, want one", count)
	}

	updated, _ = model.submitLine("介绍一下你自己")
	model = updated.(*Model)
	count = 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && user.Raw == "介绍一下你自己" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("second real submission user blocks = %d, want two", count)
	}
}
