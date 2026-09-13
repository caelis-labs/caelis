package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// codexApprovalNotice is the real automatic approval review notice Codex emits;
// it carries no structural prefix of its own.
const codexApprovalNotice = "Automatic approval review approved (risk: low, authorization: high): go test ./surfaces/tui/app"

func noticeTestContext(width int) (*Model, BlockRenderContext) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	return model, BlockRenderContext{Width: width, TermWidth: width, Theme: model.theme}
}

func noticeBodyOfRow(row RenderedRow) string {
	if body, ok := strings.CutPrefix(row.Plain, "• "); ok {
		return body
	}
	return strings.TrimPrefix(row.Plain, "  ")
}

// TestACPNoticeRendersWithSharedGutter pins the notice gutter: the shared
// bullet leads the first line, wrapped continuations align under the body, and
// the notice body text survives unchanged.
func TestACPNoticeRendersWithSharedGutter(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{name: "single line", text: codexApprovalNotice, width: 120},
		{name: "narrow ascii wrap", text: codexApprovalNotice, width: 28},
		{name: "narrow cjk wrap", text: "自动审批复核通过（风险：低，授权：高）：继续执行用户请求的命令", width: 20},
		{name: "literal indentation and blank line", text: "MCP init summary: ready\n\n  indented continuation", width: 120,
			want: []string{"• MCP init summary: ready", "  ", "    indented continuation"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ctx := noticeTestContext(tc.width)
			rows := renderACPNoticeRows("block-1", SubagentEvent{Kind: SENotice, Text: tc.text}, tc.width, ctx)
			if len(rows) == 0 {
				t.Fatal("notice rendered no rows")
			}
			mark := renderACPTranscriptHeaderMark(ctx, acpHeaderMarkDefault, false)
			var bodies strings.Builder
			for i, row := range rows {
				if displayColumns(row.Plain) > tc.width {
					t.Fatalf("row %d width = %d, want at most %d: %q", i, displayColumns(row.Plain), tc.width, row.Plain)
				}
				if row.selectionIndent != 2 || !row.PreWrapped || row.ACPHeader {
					t.Fatalf("row %d selectionIndent=%d prewrapped=%v header=%v, want 2/true/false", i, row.selectionIndent, row.PreWrapped, row.ACPHeader)
				}
				if got := ansi.Strip(row.Styled); got != row.Plain {
					t.Fatalf("row %d styled strips to %q, want %q", i, got, row.Plain)
				}
				body := noticeBodyOfRow(row)
				if i == 0 {
					if !strings.HasPrefix(row.Plain, "• ") {
						t.Fatalf("first row lost the shared mark: %q", row.Plain)
					}
					if want := mark + " " + ctx.Theme.TranscriptMetaStyle().Render(body); row.Styled != want {
						t.Fatalf("first row styled = %q, want the separately styled bullet %q", row.Styled, want)
					}
				} else if !strings.HasPrefix(row.Plain, "  ") {
					t.Fatalf("continuation row %d gutter = %q, want two leading spaces", i, row.Plain)
				}
				bodies.WriteString(body)
			}
			if got, want := bodies.String(), strings.ReplaceAll(tc.text, "\n", ""); got != want {
				t.Fatalf("notice body changed:\n got %q\nwant %q", got, want)
			}
			if tc.want != nil && renderedRowsPlain(rows) != strings.Join(tc.want, "\n") {
				t.Fatalf("notice whitespace changed: %#v, want %#v", renderedPlainRows(rows), tc.want)
			}
		})
	}
}

func TestACPStyledNoticePreservesExistingPresentation(t *testing.T) {
	_, ctx := noticeTestContext(24)
	for _, text := range []string{"warn: caution before continuing", "error: command failed", "note: tools available", "Connection ready"} {
		kind := tuikit.DetectLineStyle(text)
		if kind == tuikit.LineStyleDefault {
			t.Fatalf("fixture is not a styled notice: %q", text)
		}
		rows := renderACPNoticeRows("block", SubagentEvent{Kind: SENotice, Text: text}, 24, ctx)
		if len(rows) != 1 || rows[0].Plain != text || rows[0].Styled != tuikit.ColorizeLogLine(text, kind, ctx.Theme) {
			t.Fatalf("styled notice changed: %#v", rows)
		}
	}
}

func TestACPNoticeCopyTextDropsTheGutter(t *testing.T) {
	_, ctx := noticeTestContext(120)
	rows := renderACPNoticeRows("block-1", SubagentEvent{Kind: SENotice, Text: codexApprovalNotice}, 120, ctx)
	if got := sliceByDisplayColumns(rows[0].Plain, rows[0].selectionIndent, displayColumns(rows[0].Plain)); got != codexApprovalNotice {
		t.Fatalf("notice copy text = %q, want the notice body without the decorative mark", got)
	}
}

// TestACPNoticeGutterInRenderedFrames proves the gutter in complete physical
// VT frames for both transcript surfaces: the main timeline and the subagent
// overlay.
func TestACPNoticeGutterInRenderedFrames(t *testing.T) {
	main := NewModel(Config{NoColor: true, NoAnimation: true})
	main.Update(tea.WindowSizeMsg{Width: 64, Height: 20})
	main = applyACPEnvelopeForTest(t, main, eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Notice: codexApprovalNotice, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
	})
	main.drainPendingRenderEvents(time.Now())
	main.flushPendingViewportSync()
	assertNoticeFrameGutter(t, "main timeline", main)

	overlay := NewModel(Config{NoColor: true, NoAnimation: true})
	overlay.Update(tea.WindowSizeMsg{Width: 64, Height: 20})
	overlay.currentSessionID = "session-1"
	overlay.taskStreamWanted["task-1"] = true
	overlay.taskStreamTokens["task-1"] = 7
	overlay.taskStreamCallIDsByID["task-1"] = "spawn-1"
	overlay.taskStreamIDsByCallID["spawn-1"] = "task-1"
	view := overlay.ensureSubagentOutputView("spawn-1")
	view.taskHandle, view.actor = "orbit", "orbit[codex]"
	envelope := subagentMailboxEnvelope(t, "activity-1", time.Unix(100, 0), nil)
	envelope.Kind, envelope.Notice = eventstream.KindNotice, codexApprovalNotice
	next, _ := overlay.handleTaskStreamBatch(taskStreamBatchMsg{sessionID: "session-1", taskID: "task-1", token: 7, events: []eventstream.Envelope{envelope}})
	overlay = next.(*Model)
	view.prepareVisibleRender()
	if !overlay.openSubagentOutputOverlayView("spawn-1", view) {
		t.Fatal("subagent overlay did not open for the running Task")
	}
	assertNoticeFrameGutter(t, "subagent overlay", overlay)
}

func assertNoticeFrameGutter(t *testing.T, scope string, model *Model) {
	t.Helper()
	frame := model.View().Content
	updates := renderFullscreenFramesForTest(t, model.width, model.height, frame)
	assertPhysicalFullscreenFrame(t, model.width, model.height, frame, updates)

	lines := strings.Split(ansi.Strip(frame), "\n")
	firstIndex, bodyColumn := -1, -1
	for i, line := range lines {
		line = strings.TrimRight(line, " ")
		if idx := strings.Index(line, "Automatic approval review approved"); idx >= 0 {
			firstIndex, bodyColumn = i, displayColumns(line[:idx])
			if !strings.HasSuffix(strings.TrimRight(line[:idx], " "), "•") {
				t.Fatalf("%s: notice lost the shared mark: %q", scope, line)
			}
			break
		}
	}
	if firstIndex < 0 {
		t.Fatalf("%s: notice missing from frame:\n%s", scope, frame)
	}
	if firstIndex+1 >= len(lines) {
		t.Fatalf("%s: notice has no wrapped continuation line:\n%s", scope, frame)
	}
	continuation := lines[firstIndex+1]
	indent := displayColumns(continuation) - displayColumns(strings.TrimLeft(continuation, " "))
	if indent != bodyColumn {
		t.Fatalf("%s: continuation indent = %d, want %d aligned under the body:\n%s", scope, indent, bodyColumn, frame)
	}
}
