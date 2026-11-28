package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/charmbracelet/x/ansi"
)

func TestDesktopWorldReasoningLiveReplay(t *testing.T) {
	for _, width := range []int{24, 112} {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/nested=%t", width, nested), func(t *testing.T) {
				const height = 48
				m := newACPEventstreamRegressionModel(t, width, height)
				m.currentSessionID = "desktop-fixture"
				ctx := m.blockRenderContext(width)
				var frames []string
				captureFrame := func() string {
					m.syncViewportContent()
					frame := m.View().Content
					frames = append(frames, frame)
					return ansi.Strip(frame)
				}
				t.Cleanup(func() {
					updates := renderFullscreenFramesForTest(t, width, height, frames...)
					for i, frame := range frames {
						assertPhysicalFullscreenFrame(t, width, height, frame, updates[:i+1])
					}
				})
				var history []*session.Event
				delivered := 0
				apply := func(events ...*session.Event) string {
					history = append(history, events...)
					envelopes := desktopTraceEnvelopes(history, m.currentSessionID)
					for _, env := range envelopes[delivered:] {
						m.Update(env)
					}
					delivered = len(envelopes)
					return joinRenderedPlain(requireMainACPTurnBlockForTest(t, m).Render(ctx))
				}
				replay := func(want string) (*Model, Block) {
					t.Helper()
					replayed, block := desktopTraceModel(t, history, width, true)
					if got := joinRenderedPlain(block.Render(replayed.blockRenderContext(width))); got != want {
						t.Fatalf("replay differs from live:\n%s\nwant:\n%s", got, want)
					}
					return replayed, block
				}

				first := desktopTraceExec("first", "One", `return await app.click("Open");`)
				second := desktopTraceExec("second", "Two", `return await app.keypress({keys:["ENTER"]});`)
				last := desktopTraceExec("last", "End", `return await app.click("Close");`)
				const source = "await dtw__exec(input);"
				script := &session.EventTool{ID: "script", Name: "js_exec", Status: "pending", Input: map[string]any{"code": source}}
				if nested {
					second.Parent = &session.EventParentTool{Kind: "code_mode", CallID: script.ID, Name: script.Name}
				}

				const head = "Think."
				const middle = "\nCheck role.\nCheck bounds.\nCheck window."
				const tail = "\nThen press Enter.\nKeep result."
				reasoning := func(text string) *session.Event {
					message := model.NewReasoningMessage(model.RoleAssistant, text, model.ReasoningVisibilityVisible)
					return &session.Event{Type: session.EventTypeAssistant, MessageID: "thought", Message: &message}
				}
				stream := func(text string) *session.Event {
					ev := reasoning(text)
					ev.Protocol = &session.EventProtocol{Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentThought), MessageID: ev.MessageID,
						Content: session.ProtocolTextContent(text),
					}}
					return session.MarkUIOnly(ev)
				}

				apply(desktopTraceToolStart(first), desktopTraceResult(first, "completed", desktopTraceResultJSON))
				for _, event := range []*session.Event{stream(head), stream(middle + tail), reasoning(head + middle + tail)} {
					plain := apply(event)
					requireDesktopTraceOrder(t, plain, "• Using computer", " in One", head)
					if strings.Contains(plain, "• Used computer") {
						t.Fatalf("reasoning closed the live group:\n%s", plain)
					}
				}
				if plain := apply(); !strings.Contains(plain, "... +2 lines") || !strings.Contains(plain, "Keep result.") {
					t.Fatalf("reasoning did not retain its live preview:\n%s", plain)
				}
				replay(apply())
				requireDesktopTraceOrder(t, captureFrame(), "• Using computer", head, "Keep result.")

				if nested {
					requireDesktopTraceOrder(t, apply(desktopTraceToolStart(script)), "• Using computer", " in One", head, "Running script")
				}
				collapsed := apply(desktopTraceToolStart(second))
				requireDesktopTraceOrder(t, collapsed, "• Using computer", " in One", " in Two")
				if strings.Count(collapsed, "computer") != 1 || strings.Contains(collapsed, head) || strings.Contains(collapsed, "script") {
					t.Fatalf("the next DTW call did not absorb the reasoning:\n%s", collapsed)
				}
				collapsed = apply(desktopTraceResult(second, "completed", desktopTraceResultJSON))
				if nested {
					done := desktopTraceResult(script, "completed", `{"script":"ok"}`)
					done.Meta = map[string]any{"code_mode_script_completion": "completed"}
					collapsed = apply(done)
				}

				if frame := captureFrame(); strings.Contains(frame, head) || !strings.Contains(frame, "• Using computer") {
					t.Fatalf("settled reasoning remained visible in the collapsed frame:\n%s", frame)
				}
				token := acpStableExplorationClickToken("computer:first")
				block := requireMainACPTurnBlockForTest(t, m)
				clickScriptToken(t, m, block, ctx, token)
				expanded := joinRenderedPlain(block.Render(ctx))
				requireDesktopTraceOrder(t, expanded, " in One", head, "Check role.", "Check window.", "Keep result.", " in Two")
				requireDesktopTraceOrder(t, captureFrame(), "Click", head, "Keep result.", "Press")
				replayed, replayBlock := replay(collapsed)
				replayCtx := replayed.blockRenderContext(width)
				clickScriptToken(t, replayed, replayBlock, replayCtx, token)
				if got := joinRenderedPlain(replayBlock.Render(replayCtx)); got != expanded {
					t.Fatalf("expanded replay differs from live:\n%s\nwant:\n%s", got, expanded)
				}
				clickScriptToken(t, m, block, ctx, token)

				command := &session.EventTool{ID: "command", Name: "RunCommand", Status: "pending", Input: map[string]any{"command": "echo split"}}
				requireDesktopTraceOrder(t, apply(desktopTraceToolStart(command)), "• Used computer", " in Two", "echo split")
				plain := apply(desktopTraceToolStart(last), desktopTraceResult(last, "completed", desktopTraceResultJSON))
				requireDesktopTraceOrder(t, plain, "• Used computer", " in Two", "echo split", "• Using computer", " in End")
				replay(plain)
				requireDesktopTraceOrder(t, captureFrame(), "• Used computer", "echo split", "• Using computer")

				if !nested {
					return
				}
				// Late evidence revises the script at its original position.
				// The reasoning must reappear before the now-standalone script.
				read := &session.EventTool{ID: "read", Name: "Read", Parent: second.Parent, Status: "pending", Input: map[string]any{"path": "fixture.md"}}
				plain = apply(desktopTraceToolStart(read))
				requireDesktopTraceOrder(t, plain, "• Used computer", " in One", head, "Ran script", "Press", "Read fixture.md", "echo split", "• Using computer")
				failure := desktopTraceResult(script, "failed", `{"error":"ReferenceError: fixture"}`)
				failure.Meta = map[string]any{"code_mode_script_completion": "failed"}
				plain = apply(failure)
				requireDesktopTraceOrder(t, plain, head, "Ran script", "ReferenceError", "Press", "Read fixture.md", "echo split", " in End")
				if strings.Count(plain, head) != 1 || width == 112 && !strings.Contains(plain, source) {
					t.Fatalf("reclassified script lost input or duplicated reasoning:\n%s", plain)
				}
				replay(plain)
				requireDesktopTraceOrder(t, captureFrame(), head, "Ran script", "ReferenceError", "echo split", "• Using computer")
			})
		}
	}
}

func TestDesktopWorldReasoningWithoutSummarizedCalls(t *testing.T) {
	first := desktopTraceExec("first", "First", `if (ready) await app.click("Open");`)
	second := desktopTraceExec("second", "Second", `return 1;`)
	thought := model.NewReasoningMessage(model.RoleAssistant, "Keep this reasoning.", model.ReasoningVisibilityVisible)
	events := []*session.Event{
		desktopTraceToolStart(first), desktopTraceResult(first, "completed", desktopTraceResultJSON),
		{Type: session.EventTypeAssistant, Message: &thought},
		desktopTraceToolStart(second), desktopTraceResult(second, "completed", desktopTraceResultJSON),
	}
	for _, replay := range []bool{false, true} {
		m, block := desktopTraceModel(t, events, 60, replay)
		plain := joinRenderedPlain(block.Render(m.blockRenderContext(60)))
		if strings.Contains(plain, "computer") || !strings.Contains(plain, "Keep this reasoning.") {
			t.Fatalf("replay=%t, unrecognized actions must leave reasoning visible:\n%s", replay, plain)
		}
	}
}

func TestDesktopWorldObservationSummaryDeduplication(t *testing.T) {
	first := desktopTraceExec("first", "First", "return await app.look();")
	second := desktopTraceExec("second", "Second", "return await app.look();")
	duplicate := desktopTraceExec("duplicate", "First", "return await app.look();")
	events := []*session.Event{
		desktopTraceToolStart(first), desktopTraceResult(first, "completed", desktopTraceResultJSON),
		desktopTraceToolStart(second), desktopTraceResult(second, "completed", desktopTraceResultJSON),
		desktopTraceToolStart(duplicate), desktopTraceResult(duplicate, "completed", desktopTraceResultJSON),
	}
	for _, replay := range []bool{false, true} {
		m, block := desktopTraceModel(t, events, 24, replay)
		ctx := m.blockRenderContext(24)
		plain := joinRenderedPlain(block.Render(ctx))
		requireDesktopTraceOrder(t, plain, "• Using computer", " in Second", " in First")
		if strings.Count(plain, " in First") != 1 {
			t.Fatalf("replay=%t, repeated observation used a summary row:\n%s", replay, plain)
		}
		clickScriptToken(t, m, block, ctx, acpStableExplorationClickToken("computer:first"))
		requireDesktopTraceOrder(t, joinRenderedPlain(block.Render(ctx)), " in First", " in Second", " in First")
	}
}

func desktopTraceExec(id, app, source string) *session.EventTool {
	return desktopTraceFact(id, "dtw", "exec", "desktop-world", map[string]any{"app": app, "source": source})
}

func requireDesktopTraceOrder(t *testing.T, text string, parts ...string) {
	t.Helper()
	rest := text
	for _, part := range parts {
		_, after, found := strings.Cut(rest, part)
		if !found {
			t.Fatalf("missing %q in order %q:\n%s", part, parts, text)
		}
		rest = after
	}
}
