package tuiapp

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestReceivedMessageContinuationPhysicalColors(t *testing.T) {
	for _, width := range []int{12, 24, 40} {
		for _, dark := range []bool{true, false} {
			for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI} {
				for _, source := range []string{"parent", "reviewer[orbit]", "user"} {
					t.Run(fmt.Sprintf("%d/dark=%v/profile=%d/%s", width, dark, profile, source), func(t *testing.T) {
						model := NewModel(Config{ColorProfile: profile, NoAnimation: true})
						model.theme = tuikit.ResolveThemeWithState(dark, false, profile)
						view := model.ensureSubagentOutputView("spawn")
						body := "请验证 reconnect 消息顺序。\n\nparent: @reviewer [literal] " + strings.Repeat("多行正文。", 5)
						env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeSubagent,
							Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateUserMessage, MessageID: "input-1", Content: eventstream.TextContent{Type: "text", Text: body}}}
						if source != "user" {
							kind := "participant"
							if source == "parent" {
								kind = "controller"
							}
							env.AgentCommunicationSource = &eventstream.ActorIdentity{Kind: kind, ID: source, Name: source}
						}
						for _, event := range model.projectACPEventToTranscriptEvents(env) {
							view.observeChildEvent(event)
						}
						ctx := model.blockRenderContext(width)
						rows := view.block.Render(ctx)
						wrapped := model.wrapRenderedRowsForViewport(view.block, rows, width, ctx)
						assertMessagePhysicalColors(t, wrapped.styledLines, width, source, body, model.theme)
					})
				}
			}
		}
	}
}

func TestExpandedAgentMessageContinuationColorsAndSelection(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := model.blockRenderContext(24)
	body := strings.Repeat("wrapped message 正文 ", 8) + "\nlast line"
	event := SubagentEvent{Kind: SEAgentCommunication, SourceName: "reviewer[orbit]", MessageID: "message-1", Text: body}
	rows := renderAgentCommunicationRows("block", event, 0, 24, ctx, acpTranscriptRenderOptions{
		AgentMessageExpanded: func(string) bool { return true },
	})
	for i, row := range rows {
		if !row.PreWrapped || row.selectionIndent != 2 || row.ClickToken != agentMessageFoldClickToken("message:message-1") || row.BlockID != "block" {
			t.Fatalf("row %d lost wrapping/selection/click metadata: %#v", i, row)
		}
		if i > 0 && !strings.HasPrefix(row.Plain, "  ") {
			t.Fatalf("continuation lost gutter: %q", row.Plain)
		}
	}
	wrapped := model.wrapRenderedRowsForViewport(NewMainACPTurnBlock("turn"), rows, 24, ctx)
	assertMessagePhysicalColors(t, wrapped.styledLines, 24, "reviewer[orbit]", body, model.theme)
}

// Each row starts from reset SGR state: viewport scrolling and overlay borders
// must not supply styles inherited from the previous physical row.
func assertMessagePhysicalColors(t *testing.T, styled []string, width int, source, body string, theme tuikit.Theme) {
	t.Helper()
	type coloredText struct {
		text string
		fg   color.Color
	}
	spans := []coloredText{{"•", theme.ToolFg}, {source, theme.AgentMessageReceivedStyle().GetForeground()}, {": " + body, theme.TextStyle().GetForeground()}}
	switch source {
	case "reviewer[orbit]":
		spans = []coloredText{{"•", theme.ToolFg}, {"reviewer", theme.AgentMessageReceivedStyle().GetForeground()}, {"[orbit]", theme.SecondaryTextStyle().GetForeground()}, {": " + body, theme.TextStyle().GetForeground()}}
	case "user":
		spans = []coloredText{{">", theme.PromptStyle().GetForeground()}, {body, theme.TextStyle().GetForeground()}}
		// Basic ANSI user surfaces intentionally omit chrome and colors.
		if !userNarrativeChrome(theme) {
			spans = []coloredText{{">" + body, nil}}
		}
	}
	var want []coloredText
	for _, span := range spans {
		fg := span.fg
		if fg != nil {
			fg = colorprofile.ANSI256.Convert(fg)
		}
		for _, cluster := range splitGraphemeClusters(span.text) {
			if strings.TrimSpace(cluster) != "" {
				want = append(want, coloredText{cluster, fg})
			}
		}
	}
	painted := make([]string, len(styled))
	for i, line := range styled {
		if displayColumns(line) > width {
			t.Fatalf("row exceeds width %d: %q", width, line)
		}
		painted[i] = "\x1b[m" + line + "\x1b[m"
	}
	height := len(styled) + 1
	frame, _ := normalizeFullscreenFrameWithTopTrim(strings.Join(painted, "\n"), width, height)
	previous, _ := normalizeFullscreenFrameWithTopTrim("previous frame", width, height)
	updates := renderFullscreenFramesForTest(t, width, height, previous, frame)
	assertPhysicalFullscreenFrame(t, width, height, frame, updates)
	terminal := vt.NewSafeEmulator(width, height)
	t.Cleanup(func() { _ = terminal.Close() })
	for _, update := range updates {
		if _, err := terminal.Write([]byte(update)); err != nil {
			t.Fatal(err)
		}
	}
	index := 0
	for y := range len(styled) {
		for x := 0; x < width; x++ {
			cell := terminal.CellAt(x, y)
			if cell == nil || cell.Width == 0 || strings.TrimSpace(cell.Content) == "" {
				continue
			}
			if index >= len(want) {
				t.Fatalf("unexpected cell %d,%d: %#v", x, y, cell)
			}
			matchesColor := cell.Style.Fg == nil && want[index].fg == nil || colorInSet(cell.Style.Fg, []color.Color{want[index].fg})
			if cell.Content != want[index].text || !matchesColor {
				t.Fatalf("cell %d,%d = %#v, want %#v\nframe:\n%s", x, y, cell, want[index], ansi.Strip(terminal.Render()))
			}
			index++
		}
	}
	if index != len(want) {
		t.Fatalf("rendered %d glyphs, want %d", index, len(want))
	}
}
