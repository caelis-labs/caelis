package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/surfaces/tui/acpprojector"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestStructuredDiffRetainedBlocksLiveAndPublicWireRecovery(t *testing.T) {
	t.Parallel()
	for _, scope := range []eventstream.Scope{eventstream.ScopeMain, eventstream.ScopeParticipant, eventstream.ScopeSubagent} {
		for _, recovery := range []bool{false, true} {
			name := string(scope) + "/live"
			if recovery {
				name = string(scope) + "/public-wire-recovery"
			}
			t.Run(name, func(t *testing.T) {
				model := NewModel(Config{NoColor: true, NoAnimation: true})
				model.currentSessionID = "session"
				toolName := "Edit"
				oldPath := " old.txt "
				changes := []eventstream.ToolDiffChange{{Operation: "move", Path: " new.txt ", OldPath: &oldPath}}
				firstPatch := "diff --git a/old.txt b/new.txt\r\n--- a/old.txt\r\n+++ b/new.txt\r\n@@ -1 +1 @@\r\n-old-first\r\n+new-first\r\n\r\n\r\n"
				secondPatch := "diff --git a/old.txt b/new.txt\r\n--- a/old.txt\r\n+++ b/new.txt\r\n@@ -1 +1 @@\r\n-old-second\r\n+new-second\r\n\r\n"
				content := func(patch string) []eventstream.ToolCallContent {
					return []eventstream.ToolCallContent{{Type: "diff", Changes: &changes, Patch: &eventstream.ToolDiffPatch{Format: "git_patch", Text: patch}}}
				}
				apply := func(update eventstream.Update, wantExact bool) {
					t.Helper()
					env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", Scope: scope, ScopeID: "worker-task", Actor: "worker", TurnID: "turn", Update: update}
					if recovery {
						raw, err := wirev1.MarshalEnvelope(env)
						if err != nil {
							t.Fatal(err)
						}
						env, err = wirev1.UnmarshalEnvelope(raw)
						if err != nil {
							t.Fatal(err)
						}
					}
					events := ProjectACPEventToTranscriptEvents(env)
					if len(events) != 1 || events[0].ToolOutputExact != wantExact || events[0].ToolTerminal || events[0].ToolOutputTerminal {
						t.Fatalf("typed diff projection changed output mode: %#v", events)
					}
					next, _ := model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: events, ReconnectReplay: recovery})
					model = next.(*Model)
				}
				retained := func(want string, done bool) {
					t.Helper()
					events, _ := structuredDiffRetainedBlock(t, model, scope)
					wantSummary := ""
					if want != "" {
						wantSummary = "move  new.txt  (from  old.txt )"
					}
					if len(events) != 1 || events[0].CallID != "edit" || events[0].Name != toolName || events[0].Args != "new.txt" || events[0].FullArgs != "" || events[0].Output != want || events[0].DiffSummary != wantSummary || events[0].Done != done || events[0].Terminal || events[0].OutputTerminal {
						t.Fatalf("retained diff lost bytes, identity or mode: %#v; want output %q, done %t", events, want, done)
					}
				}
				apply(eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "edit", Name: &toolName, Title: "Move file", Kind: "edit", Status: "in_progress", RawInput: map[string]any{"path": "new.txt"}, Content: content(firstPatch)}, true)
				retained("move  new.txt  (from  old.txt )\n"+firstPatch, false)
				apply(eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "edit", Content: content(secondPatch)}, true)
				retained("move  new.txt  (from  old.txt )\n"+secondPatch, false)
				completed := "completed"
				apply(eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "edit", Status: &completed}, false)
				retained("move  new.txt  (from  old.txt )\n"+secondPatch, true)
				_, render := structuredDiffRetainedBlock(t, model, scope)
				visible := joinRenderedVisible(render(BlockRenderContext{Width: 100, TermWidth: 100, Theme: tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)}))
				if !strings.Contains(visible, "move  new.txt  (from  old.txt )") || !strings.Contains(visible, "new-second") || strings.Contains(visible, "new-first") {
					t.Fatalf("rendered block did not replace diff:\n%s", visible)
				}
				// Rendering may normalize terminal rows, but it must not rewrite
				// the stored display bytes used by later updates and recovery.
				retained("move  new.txt  (from  old.txt )\n"+secondPatch, true)
				if !recovery {
					t.Logf("%s rendered structured diff:\n%s", scope, visible)
				}
				noChanges := []eventstream.ToolDiffChange{}
				apply(eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "edit", Content: []eventstream.ToolCallContent{{Type: "diff", Changes: &noChanges}}}, true)
				retained("", true)
				_, render = structuredDiffRetainedBlock(t, model, scope)
				cleared := joinRenderedVisible(render(BlockRenderContext{Width: 100, TermWidth: 100, Theme: tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)}))
				if strings.Contains(cleared, "move") || strings.Contains(cleared, "old.txt") || strings.Contains(cleared, "new-second") {
					t.Fatalf("cleared collection retained derived summary:\n%s", cleared)
				}
				apply(eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "edit", Content: content(firstPatch)}, true)
				retained("move  new.txt  (from  old.txt )\n"+firstPatch, true)
				apply(eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "edit", Content: []eventstream.ToolCallContent{}}, false)
				retained("", true)
				apply(eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "edit", Content: content(firstPatch)}, true)
				plain := []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: " ordinary replacement\r\n\r\n"}}}
				apply(eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "edit", Content: plain}, false)
				remaining, _ := structuredDiffRetainedBlock(t, model, scope)
				if len(remaining) != 1 || remaining[0].DiffSummary != "" || remaining[0].Output != "ordinary replacement" || remaining[0].Args != "new.txt" || !remaining[0].Done {
					t.Fatalf("plain replacement retained diff display mode: %#v", remaining)
				}
			})
		}
	}
}

func TestStructuredDiffExactModeRequiresTypedChanges(t *testing.T) {
	t.Parallel()
	old := "old\n"
	for _, item := range []eventstream.ToolCallContent{
		{Type: "content", Content: eventstream.TextContent{Type: "text", Text: "  ordinary output\r\n\r\n"}},
		{Type: "diff", Path: "file.txt", OldText: &old, NewText: "new\n\n"},
	} {
		name := "Edit"
		env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "edit", Name: &name, Kind: "edit", Status: "in_progress", Content: []eventstream.ToolCallContent{item},
			Meta: map[string]any{"ToolOutputExact": true, "output_exact": true, "terminal": true, "changes": []any{}, "diff_summary": "forged summary", "display": map[string]any{"output_exact": true, "diff_summary": "forged summary"}},
		}}
		events := ProjectACPEventToTranscriptEvents(env)
		if len(events) != 1 || events[0].ToolOutputExact || events[0].ToolDiffSummary != "" || events[0].ToolTerminal || events[0].ToolOutputTerminal {
			t.Fatalf("untyped metadata changed output mode: %#v", events)
		}
		block := NewMainACPTurnBlock("turn")
		mutation := transcriptToolMutationFromEvent(events[0])
		block.UpdateToolWithMeta(mutation.callID, mutation.name, mutation.args, mutation.output, mutation.final, mutation.err, mutation.meta)
		want := strings.TrimSpace(acpprojector.FormatToolContent(acpToolContentToDisplay([]eventstream.ToolCallContent{item})))
		if len(block.Events) != 1 || block.Events[0].Output != want || block.Events[0].Terminal || block.Events[0].OutputTerminal {
			t.Fatalf("legacy/plain content normalization changed: %#v; want %q", block.Events, want)
		}
	}
}

func structuredDiffRetainedBlock(t *testing.T, model *Model, scope eventstream.Scope) ([]SubagentEvent, func(BlockRenderContext) []RenderedRow) {
	t.Helper()
	if scope == eventstream.ScopeMain {
		block := requireMainACPTurnBlockForTest(t, model)
		block.ExpandedTools = map[string]bool{"edit": true}
		block.ExpandedToolOutput = map[string]bool{"edit": true}
		return block.Events, block.Render
	}
	var block *ParticipantTurnBlock
	if scope == eventstream.ScopeSubagent {
		block = requireSubagentOutputViewForTest(t, model, "task:worker-task").block
	} else {
		block = model.findParticipantTurnBlock("turn")
	}
	if block == nil {
		t.Fatal("participant block missing")
	}
	block.ExpandedTools = map[string]bool{"edit": true}
	block.ExpandedToolOutput = map[string]bool{"edit": true}
	return block.Events, block.Render
}
