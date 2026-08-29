package acpbridge

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestFinalAssistantAccumulatorKeepsLastAssistantStep(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	first := acc.ObserveUpdate(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "I will inspect."}})
	if !first.Assistant || first.Delta != "I will inspect." || first.MessageID == "" || acc.FinalText() != "I will inspect." {
		t.Fatalf("first update = %#v final = %q", first, acc.FinalText())
	}
	barrier := acc.ObserveUpdate(eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "call-1", Kind: eventstream.ToolKindExecute})
	if !barrier.Barrier || acc.FinalText() != "" {
		t.Fatalf("tool barrier = %#v final = %q, want reset", barrier, acc.FinalText())
	}
	acc.ObserveUpdate(eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "call-1"})
	final := acc.ObserveUpdate(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: map[string]any{"text": "Final answer."}})
	if !final.Assistant || final.Delta != "Final answer." || final.MessageID == "" || final.MessageID == first.MessageID || acc.FinalText() != "Final answer." {
		t.Fatalf("final update = %#v final = %q, want a new post-barrier identity", final, acc.FinalText())
	}
}

func TestFinalAssistantAccumulatorTreatsThoughtAndPlanAsBarriers(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	acc.ObserveUpdate(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "progress"}})
	if got := acc.ObserveUpdate(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentThought, Content: eventstream.TextContent{Type: "text", Text: "thinking"}}); !got.Barrier {
		t.Fatalf("thought update = %#v, want barrier", got)
	}
	if acc.FinalText() != "" {
		t.Fatalf("final after thought = %q, want empty", acc.FinalText())
	}
	acc.ObserveUpdate(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "more progress"}})
	if got := acc.ObserveUpdate(eventstream.PlanUpdate{SessionUpdate: eventstream.UpdatePlan, Entries: []eventstream.PlanEntry{{Content: "run tests"}}}); !got.Barrier {
		t.Fatalf("plan update = %#v, want barrier", got)
	}
	if acc.FinalText() != "" {
		t.Fatalf("final after plan = %q, want empty", acc.FinalText())
	}
}

func TestFinalAssistantAccumulatorIgnoresControlUpdates(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	acc.ObserveUpdate(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "final"}})
	got := acc.ObserveUpdate(eventstream.RawUpdate{SessionUpdate: "usage_update"})
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
	first := acc.ObserveUpdate(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "hel"}})
	second := acc.ObserveUpdate(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "hello"}})
	third := acc.ObserveUpdate(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "lo"}})
	if first.Delta != "hel" || second.Delta != "hello" || third.Delta != "lo" ||
		first.MessageID == "" || second.MessageID != first.MessageID || third.MessageID != first.MessageID ||
		acc.FinalText() != "helhellolo" {
		t.Fatalf("updates = %#v/%#v/%#v final = %q", first, second, third, acc.FinalText())
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
				got := acc.ObserveUpdate(eventstream.ContentChunk{
					SessionUpdate: eventstream.UpdateAgentMessage,
					MessageID:     "message-1",
					Content:       eventstream.TextContent{Type: "text", Text: frame},
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
		got := acc.ObserveUpdate(eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			MessageID:     "message-1",
			Content:       eventstream.TextContent{Type: "text", Text: frame},
		})
		if got.Delta != frame {
			t.Fatalf("frame %d delta = %q, want exact %q", i, got.Delta, frame)
		}
	}
	if got := acc.FinalText(); got != "helhellohello world" {
		t.Fatalf("FinalText() = %q, want exact appended frames", got)
	}
}

func TestFinalAssistantAccumulatorKeepsGeneratedIdentityWhenProviderIDAppearsMidStream(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	first := acc.ObserveFrame("", "hello ")
	second := acc.ObserveFrame("provider-message-1", "world")
	if first.MessageID == "" || second.MessageID != first.MessageID {
		t.Fatalf("message ids = %q / %q, want generated identity retained until a barrier", first.MessageID, second.MessageID)
	}
	if got := acc.FinalText(); got != "hello world" {
		t.Fatalf("FinalText() = %q, want anonymous prefix retained", got)
	}

	acc.Reset()
	third := acc.ObserveFrame("provider-message-2", "next")
	if third.MessageID != "provider-message-2" || acc.FinalText() != "next" {
		t.Fatalf("post-reset update = %#v final = %q, want provider identity accepted after barrier", third, acc.FinalText())
	}
}

func TestGeneratedNarrativeMessageIDsAreUniqueAcrossProcesses(t *testing.T) {
	const helperEnvironment = "CAELIS_TEST_GENERATED_NARRATIVE_MESSAGE_ID"
	if os.Getenv(helperEnvironment) == "1" {
		_, _ = os.Stdout.WriteString(nextGeneratedNarrativeMessageID())
		os.Exit(0)
	}

	generate := func() string {
		t.Helper()
		command := exec.Command(os.Args[0], "-test.run=^TestGeneratedNarrativeMessageIDsAreUniqueAcrossProcesses$")
		command.Env = append(os.Environ(), helperEnvironment+"=1")
		output, err := command.Output()
		if err != nil {
			t.Fatalf("generated-id helper error = %v", err)
		}
		return strings.TrimSpace(string(output))
	}
	first := generate()
	second := generate()
	if first == "" || second == "" || first == second {
		t.Fatalf("cross-process message ids = %q / %q, want distinct non-empty identities", first, second)
	}
}

func TestFinalAssistantAccumulatorSeparatesMessageIDs(t *testing.T) {
	t.Parallel()

	var acc FinalAssistantAccumulator
	first := acc.ObserveUpdate(eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentMessage,
		MessageID:     "m1",
		Content:       eventstream.TextContent{Type: "text", Text: "hello"},
	})
	second := acc.ObserveUpdate(eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentMessage,
		MessageID:     "m2",
		Content:       eventstream.TextContent{Type: "text", Text: "world"},
	})
	if first.Delta != "hello" || second.Delta != "world" || acc.FinalText() != "world" {
		t.Fatalf("updates = %#v / %#v final = %q, want message-id reset", first, second, acc.FinalText())
	}
}
