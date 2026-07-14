package schema

import "testing"

func TestFinalAssistantAccumulatorKeepsLastAssistantStep(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	first := acc.ObserveUpdate(ContentChunk{SessionUpdate: UpdateAgentMessage, Content: TextContent{Type: "text", Text: "I will inspect."}})
	if !first.Assistant || first.Delta != "I will inspect." || acc.FinalText() != "I will inspect." {
		t.Fatalf("first update = %#v final = %q", first, acc.FinalText())
	}
	barrier := acc.ObserveUpdate(ToolCall{SessionUpdate: UpdateToolCall, ToolCallID: "call-1", Kind: ToolKindExecute})
	if !barrier.Barrier || acc.FinalText() != "" {
		t.Fatalf("tool barrier = %#v final = %q, want reset", barrier, acc.FinalText())
	}
	acc.ObserveUpdate(ToolCallUpdate{SessionUpdate: UpdateToolCallInfo, ToolCallID: "call-1"})
	final := acc.ObserveUpdate(ContentChunk{SessionUpdate: UpdateAgentMessage, Content: map[string]any{"text": "Final answer."}})
	if !final.Assistant || final.Delta != "Final answer." || acc.FinalText() != "Final answer." {
		t.Fatalf("final update = %#v final = %q", final, acc.FinalText())
	}
}

func TestFinalAssistantAccumulatorTreatsThoughtAndPlanAsBarriers(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	acc.ObserveUpdate(ContentChunk{SessionUpdate: UpdateAgentMessage, Content: TextContent{Type: "text", Text: "progress"}})
	if got := acc.ObserveUpdate(ContentChunk{SessionUpdate: UpdateAgentThought, Content: TextContent{Type: "text", Text: "thinking"}}); !got.Barrier {
		t.Fatalf("thought update = %#v, want barrier", got)
	}
	if acc.FinalText() != "" {
		t.Fatalf("final after thought = %q, want empty", acc.FinalText())
	}
	acc.ObserveUpdate(ContentChunk{SessionUpdate: UpdateAgentMessage, Content: TextContent{Type: "text", Text: "more progress"}})
	if got := acc.ObserveUpdate(PlanUpdate{SessionUpdate: UpdatePlan, Entries: []PlanEntry{{Content: "run tests"}}}); !got.Barrier {
		t.Fatalf("plan update = %#v, want barrier", got)
	}
	if acc.FinalText() != "" {
		t.Fatalf("final after plan = %q, want empty", acc.FinalText())
	}
}

func TestFinalAssistantAccumulatorIgnoresControlUpdates(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	acc.ObserveUpdate(ContentChunk{SessionUpdate: UpdateAgentMessage, Content: TextContent{Type: "text", Text: "final"}})
	got := acc.ObserveUpdate(RawUpdate{SessionUpdate: "usage_update"})
	if got.Barrier || got.Assistant {
		t.Fatalf("raw update = %#v, want ignored", got)
	}
	if acc.FinalText() != "final" {
		t.Fatalf("final after raw update = %q, want unchanged", acc.FinalText())
	}
}

func TestFinalAssistantAccumulatorAppendsEveryACPDelta(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	first := acc.ObserveUpdate(ContentChunk{SessionUpdate: UpdateAgentMessage, Content: TextContent{Type: "text", Text: "hel"}})
	second := acc.ObserveUpdate(ContentChunk{SessionUpdate: UpdateAgentMessage, Content: TextContent{Type: "text", Text: "hello"}})
	third := acc.ObserveUpdate(ContentChunk{SessionUpdate: UpdateAgentMessage, Content: TextContent{Type: "text", Text: "lo"}})
	if first.Delta != "hel" || second.Delta != "hello" || third.Delta != "lo" || acc.FinalText() != "helhellolo" {
		t.Fatalf("deltas = %q/%q/%q final = %q", first.Delta, second.Delta, third.Delta, acc.FinalText())
	}
}

func TestFinalAssistantAccumulatorPreservesAllACPDeltaShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		frames     []string
		wantDeltas []string
		wantFinal  string
	}{
		{
			name:       "identical frames",
			frames:     []string{"ha", "ha", "!"},
			wantDeltas: []string{"ha", "ha", "!"},
			wantFinal:  "haha!",
		},
		{
			name:       "short prefix frame",
			frames:     []string{"hello", "hel", "!"},
			wantDeltas: []string{"hello", "hel", "!"},
			wantFinal:  "hellohel!",
		},
		{
			name:       "prefix growing frames",
			frames:     []string{"a", "ab"},
			wantDeltas: []string{"a", "ab"},
			wantFinal:  "aab",
		},
		{
			name:       "longer repeated prefix",
			frames:     []string{"ha", "ha", "haha!"},
			wantDeltas: []string{"ha", "ha", "haha!"},
			wantFinal:  "hahahaha!",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var acc FinalAssistantAccumulator
			for i, frame := range tt.frames {
				got := acc.ObserveUpdate(ContentChunk{
					SessionUpdate: UpdateAgentMessage,
					MessageID:     "message-1",
					Content:       TextContent{Type: "text", Text: frame},
				})
				if got.Delta != tt.wantDeltas[i] {
					t.Fatalf("frame %d delta = %q, want %q", i, got.Delta, tt.wantDeltas[i])
				}
			}
			if got := acc.FinalText(); got != tt.wantFinal {
				t.Fatalf("FinalText() = %q, want %q", got, tt.wantFinal)
			}
		})
	}
}

func TestFinalAssistantAccumulatorDoesNotInferCumulativeSnapshots(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	frames := []string{"hel", "hello", "hello world"}
	for i, frame := range frames {
		got := acc.ObserveUpdate(ContentChunk{
			SessionUpdate: UpdateAgentMessage,
			MessageID:     "message-1",
			Content:       TextContent{Type: "text", Text: frame},
		})
		if got.Delta != frame {
			t.Fatalf("frame %d delta = %q, want exact %q", i, got.Delta, frame)
		}
	}
	if got := acc.FinalText(); got != "helhellohello world" {
		t.Fatalf("FinalText() = %q, want exact appended frames", got)
	}
}

func TestFinalAssistantAccumulatorSeparatesMessageIDs(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	first := acc.ObserveUpdate(ContentChunk{
		SessionUpdate: UpdateAgentMessage,
		MessageID:     "m1",
		Content:       TextContent{Type: "text", Text: "hello"},
	})
	second := acc.ObserveUpdate(ContentChunk{
		SessionUpdate: UpdateAgentMessage,
		MessageID:     "m2",
		Content:       TextContent{Type: "text", Text: "world"},
	})
	if first.Delta != "hello" || second.Delta != "world" || acc.FinalText() != "world" {
		t.Fatalf("updates = %#v / %#v final = %q, want message-id reset", first, second, acc.FinalText())
	}
}
