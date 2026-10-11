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

func TestFinalAssistantAccumulatorLatestIdentifiedMessageResumesAfterBarrier(t *testing.T) {
	for _, barrier := range []eventstream.Update{
		eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "tool"},
		eventstream.PlanUpdate{SessionUpdate: eventstream.UpdatePlan},
		eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentThought, Content: eventstream.TextContent{Type: "text", Text: "thinking"}},
	} {
		t.Run(barrier.SessionUpdateType(), func(t *testing.T) {
			var acc FinalAssistantAccumulator
			acc.ObserveFrame("older", "old progress")
			acc.ObserveFrame("latest", "progress")
			acc.ObserveUpdate(barrier)
			if acc.FinalText() != "" {
				t.Fatalf("barrier retained eligible final: %q", acc.FinalText())
			}
			old := acc.ObserveFrame("older", " late delta")
			if old.Text != "" || acc.FinalText() != "" {
				t.Fatalf("older repair restored a final: %#v", old)
			}
			acc.ObserveFrame("latest", "")
			if acc.FinalText() != "" {
				t.Fatal("empty append restored a final")
			}
			latest := acc.ObserveFrame("latest", " suffix")
			if latest.MessageID != "latest" || latest.Delta != " suffix" || acc.FinalText() != "progress suffix" {
				t.Fatalf("latest identified continuation lost: %#v final=%q", latest, acc.FinalText())
			}
			acc.ObserveUpdate(barrier)
			latest = acc.ObserveReplacement("latest", "final answer")
			if latest.MessageID != "latest" || !latest.Replacement || acc.FinalText() != "final answer" {
				t.Fatalf("latest identified snapshot lost: %#v final=%q", latest, acc.FinalText())
			}
		})
	}
}

func TestFinalAssistantAccumulatorAcceptsNewProviderIdentityAfterAnonymousBarrier(t *testing.T) {
	var acc FinalAssistantAccumulator
	first := acc.ObserveFrame("", "progress")
	acc.AdvanceBoundary()
	final := acc.ObserveFrame("provider-final", "final answer")
	if first.MessageID == "" || final.MessageID != "provider-final" || final.Text != "final answer" || acc.FinalText() != "final answer" {
		t.Fatalf("new provider identity aliased an expired anonymous segment: %#v / %#v", first, final)
	}
}

func TestFinalAssistantAccumulatorActivityResetRetainsSessionIdentities(t *testing.T) {
	for _, aliased := range []bool{false, true} {
		t.Run(map[bool]string{false: "identified", true: "generated alias"}[aliased], func(t *testing.T) {
			var acc FinalAssistantAccumulator
			oldID := "previous-message"
			if aliased {
				oldID = acc.ObserveFrame("", "anonymous prefix").MessageID
			}
			acc.ObserveFrame("previous-message", "previous answer")
			checkpoint := acc
			acc.ResetActivity()
			for _, replacement := range []bool{false, true} {
				patch := acc.observeFrame("previous-message", "old revision", replacement)
				if patch.MessageID != oldID || patch.Delta != "old revision" || patch.Text != "" || acc.FinalText() != "" {
					t.Fatalf("old revision selected before new answer: %#v final=%q", patch, acc.FinalText())
				}
			}
			current := acc.ObserveFrame("current-message", "current answer")
			for _, replacement := range []bool{false, true} {
				patch := acc.observeFrame("previous-message", "", replacement)
				if patch.MessageID != oldID || patch.Text != "current answer" || acc.FinalText() != "current answer" {
					t.Fatalf("old revision stole current answer: %#v final=%q", patch, acc.FinalText())
				}
			}
			if current.MessageID != "current-message" || checkpoint.FinalText() == "" {
				t.Fatal("activity reset changed checkpoint or new answer identity")
			}
			if _, found := checkpoint.introduced["current-message"]; found {
				t.Fatal("new activity mutated the pre-dispatch checkpoint")
			}
			acc.Reset()
			reused := acc.ObserveFrame("previous-message", "new Session answer")
			if reused.MessageID != "previous-message" || reused.Text != "new Session answer" || acc.FinalText() != "new Session answer" {
				t.Fatalf("full Session reset retained old identity: %#v", reused)
			}
		})
	}
}

func TestFinalAssistantAccumulatorActivityResetPreservesAnonymousV1Segments(t *testing.T) {
	var acc FinalAssistantAccumulator
	first := acc.ObserveFrame("", "first")
	acc.ResetActivity()
	second := acc.ObserveFrame("", "second")
	if first.MessageID == "" || second.MessageID == "" || second.MessageID == first.MessageID || second.Text != "second" {
		t.Fatalf("anonymous v1 activity did not start a fresh segment: %#v / %#v", first, second)
	}
	acc.AdvanceBoundary()
	third := acc.ObserveFrame("", "post-tool")
	if third.MessageID == second.MessageID || third.Text != "post-tool" {
		t.Fatalf("anonymous v1 tool boundary changed: %#v", third)
	}
	if len(acc.introduced) != 0 {
		t.Fatal("anonymous v1 segments accumulated Session identity history")
	}
}

func TestFinalAssistantAccumulatorSessionObservationKeepsRevisionsDisplayOnly(t *testing.T) {
	var acc FinalAssistantAccumulator
	observe := func(id, text string, replacement, previous bool) AssistantTextUpdate {
		return acc.ObserveUpdate(eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage, MessageID: id,
			Content:     eventstream.TextContent{Type: "text", Text: text},
			Replacement: replacement, PreviouslyObserved: previous,
		})
	}
	for _, replacement := range []bool{false, true} {
		old := observe("previous-message", "old revision", replacement, true)
		if old.MessageID != "previous-message" || old.Delta != "old revision" || old.Text != "" || acc.FinalText() != "" {
			t.Fatalf("previous Session observation selected an answer: %#v", old)
		}
	}
	observe("current-message", "current", true, false)
	observe("current-message", " continuation", false, true)
	if acc.FinalText() != "current continuation" {
		t.Fatal("Session observation suppressed the selected current message")
	}
	old := observe("previous-message", "", true, true)
	if !old.Replacement || old.MessageID != "previous-message" || acc.FinalText() != "current continuation" {
		t.Fatalf("previous clear changed the selected answer: %#v", old)
	}
	acc.AdvanceBoundary()
	observe("current-message", "", false, true)
	if acc.FinalText() != "" {
		t.Fatal("metadata-only observation restored final eligibility")
	}
	observe("current-message", "complete current answer", true, true)
	if acc.FinalText() != "complete current answer" {
		t.Fatal("current replacement lost eligibility after a boundary")
	}
}

func TestFinalAssistantAccumulatorThoughtObservationIsScopedToActivity(t *testing.T) {
	var acc FinalAssistantAccumulator
	thought := func(previous bool) AssistantTextUpdate {
		return acc.ObserveUpdate(eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentThought, MessageID: "shared-id",
			Content: "thought", Replacement: true, PreviouslyObserved: previous,
		})
	}
	if !thought(false).Barrier {
		t.Fatal("new thought lost its barrier")
	}
	acc.ObserveFrame("shared-id", "first answer")
	if !thought(true).Barrier || acc.FinalText() != "" {
		t.Fatal("current Activity thought continuation lost its barrier")
	}
	checkpoint := acc
	acc.ResetActivity()
	acc.ObserveFrame("next-answer", "second answer")
	if thought(true).Barrier || acc.FinalText() != "second answer" {
		t.Fatal("prior Activity thought invalidated current answer")
	}
	if _, previous := checkpoint.activityThoughts["shared-id"]; !previous || len(acc.activityThoughts) != 0 {
		t.Fatal("thought Activity reset changed its checkpoint or retained ownership")
	}
	acc.Reset()
	acc.ObserveFrame("new-session-answer", "new Session answer")
	if !thought(false).Barrier || acc.FinalText() != "" {
		t.Fatal("new Session thought identity lost its barrier")
	}
}

func TestFinalAssistantAccumulatorToolObservationIsScopedToActivity(t *testing.T) {
	var acc FinalAssistantAccumulator
	acc.ObserveFrame("old-tool", "current answer")
	old := eventstream.ToolCallUpdate{ToolCallID: "old-tool", PreviouslyObserved: true}
	if acc.ObserveUpdate(old).Barrier || acc.FinalText() != "current answer" {
		t.Fatal("old tool revision invalidated an assistant with the same ID")
	}
	tool := eventstream.ToolCallUpdate{ToolCallID: "current-tool"}
	if !acc.ObserveUpdate(tool).Barrier || acc.FinalText() != "" {
		t.Fatal("new sparse tool lost its barrier")
	}
	acc.ObserveFrame("answer-two", "second answer")
	tool.PreviouslyObserved = true
	if !acc.ObserveUpdate(&tool).Barrier || acc.FinalText() != "" {
		t.Fatal("current sparse tool continuation lost its barrier")
	}
	checkpoint := acc
	acc.ResetActivity()
	acc.ObserveFrame("answer-three", "third answer")
	if acc.ObserveUpdate(tool).Barrier || acc.FinalText() != "third answer" {
		t.Fatal("prior Activity tool revision invalidated current answer")
	}
	if _, known := checkpoint.activityTools[tool.ToolCallID]; !known || len(acc.activityTools) != 0 {
		t.Fatal("tool Activity reset corrupted checkpoint or retained ownership")
	}
	acc.Reset()
	acc.ObserveFrame("new-answer", "new Session answer")
	tool.PreviouslyObserved = false
	if !acc.ObserveUpdate(tool).Barrier {
		t.Fatal("new Session tool identity lost its barrier")
	}
	acc.ObserveFrame("stable-answer", "stable answer")
	if !acc.ObserveUpdate(eventstream.ToolCallUpdate{ToolCallID: "old-tool"}).Barrier {
		t.Fatal("stable v1 tool without observation lost its barrier")
	}
}

func TestFinalAssistantAccumulatorOpaqueAliasesStayDistinctAcrossActivities(t *testing.T) {
	var acc FinalAssistantAccumulator
	generated := acc.ObserveFrame("", "original").MessageID
	if got := acc.ObserveFrame(" answer ", " suffix"); got.MessageID != generated {
		t.Fatal("anonymous v1 segment lost its provider alias")
	}
	acc.ResetActivity()
	if got := acc.ObserveReplacement("answer", "current"); got.MessageID != "answer" {
		t.Fatalf("distinct provider identity adopted old alias: %#v", got)
	}
	if got := acc.ObserveReplacement(" answer ", "old revision"); got.MessageID != generated || acc.FinalText() != "current" {
		t.Fatalf("old opaque alias stole current result: %#v final=%q", got, acc.FinalText())
	}
	acc.Reset()
	if got := acc.ObserveReplacement(" answer ", "new Session"); got.MessageID != " answer " || acc.FinalText() != "new Session" {
		t.Fatalf("full reset retained prior Session alias: %#v", got)
	}
}
