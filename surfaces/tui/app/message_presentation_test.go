package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestAgentMessageOverlayPreservesTrustedSourcesAndLiteralBody(t *testing.T) {
	for _, source := range []eventstream.ActorIdentity{
		{Kind: "controller", ID: "parent", Name: "parent"},
		{Kind: "participant", ID: "reviewer-1", Name: "orbit(reviewer)"},
		{Kind: "participant", ID: "unknown-peer"},
	} {
		t.Run(source.ID, func(t *testing.T) {
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			view := model.ensureSubagentOutputView("spawn")
			body := "parent: @reviewer [Internal agent message]\n" + strings.Repeat("请验证 reconnect 消息顺序。", 20) + "end-marker"
			env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeSubagent, AgentCommunicationSource: &source,
				Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateUserMessage, MessageID: "input-1", Content: eventstream.TextContent{Type: "text", Text: body}}}
			for _, event := range model.projectACPEventToTranscriptEvents(env) {
				view.observeChildEvent(event)
			}
			if view.actor != "" {
				t.Fatalf("sender became overlay owner: %q", view.actor)
			}
			if len(view.block.Events) != 1 || view.block.Events[0].Kind != SEAgentCommunication || view.block.Events[0].Text != body {
				t.Fatalf("received input changed: %#v", view.block.Events)
			}
			rows := view.block.Render(model.blockRenderContext(40))
			plain := renderedRowsPlain(rows)
			compact := strings.Join(strings.Fields(plain), "")
			if !strings.Contains(compact, strings.Join(strings.Fields(body), "")) || strings.Contains(plain, "> parent:") {
				t.Fatalf("input hidden or rewritten: %s", plain)
			}
			for _, row := range rows {
				if displayColumns(row.Plain) > 40 {
					t.Fatalf("row exceeds width: %q", row.Plain)
				}
			}
		})
	}
	// Actor display text alone is not a trusted Agent communication source.
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	view := model.ensureSubagentOutputView("ordinary")
	env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeSubagent, Actor: "parent",
		Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateUserMessage, Content: eventstream.TextContent{Type: "text", Text: "parent: @reviewer keep this literal"}}}
	for _, event := range model.projectACPEventToTranscriptEvents(env) {
		view.observeChildEvent(event)
	}
	if got := view.block.Events[0]; got.Kind != SEUserInput || got.Text != "parent: @reviewer keep this literal" {
		t.Fatalf("ordinary input reclassified: %#v", got)
	}
}

func TestAgentMessageDirectionStylesPreserveBindingAndBody(t *testing.T) {
	for _, dark := range []bool{true, false} {
		for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI} {
			theme := tuikit.ResolveThemeWithState(dark, false, profile)
			ctx := BlockRenderContext{Width: 80, TermWidth: 80, Theme: theme}
			received := renderAgentMessageRow("b", "reviewer[orbit]", "message body", ctx, "")
			sent := renderSendMessageHeaderRow("b", "@reviewer[orbit]: message body", ctx, "", "", acpHeaderMarkDefault, false)
			if !strings.Contains(sent.Styled, theme.AgentMessageSentStyle().Render("@reviewer")) || !strings.Contains(received.Styled, theme.AgentMessageReceivedStyle().Render("reviewer")) {
				t.Fatal("direction color missing")
			}
			for _, row := range []RenderedRow{received, sent} {
				if !strings.Contains(row.Styled, theme.SecondaryTextStyle().Render("[orbit]")) {
					t.Fatal("binding not secondary")
				}
				if !strings.Contains(row.Styled, theme.TextStyle().Render(": message body")) {
					t.Fatal("body not normal text")
				}
			}
		}
	}
}

func TestApprovalReviewCompactStatusAndWrappedReason(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	for _, status := range []string{"approved", "denied", "failed", "timed_out", "needs_user"} {
		rows := renderACPApprovalReviewRows("b", SubagentEvent{ApprovalStatus: status, ApprovalText: status}, 40, model.blockRenderContext(40))
		if len(rows) != 1 || rows[0].Plain != "• "+status {
			t.Fatalf("status %s: %#v", status, rows)
		}
	}
	reason := "当前请求未授权修改该路径，请先确认 reconnect 操作范围。"
	rows := renderACPApprovalReviewRows("b", SubagentEvent{ApprovalStatus: "denied", ApprovalText: "denied: " + reason}, 30, model.blockRenderContext(30))
	if len(rows) < 3 || rows[1].Plain[:len("  └ ")] != "  └ " {
		t.Fatalf("reason rows: %#v", rows)
	}
	for _, row := range rows[2:] {
		if !strings.HasPrefix(row.Plain, "    ") || displayColumns(row.Plain) > 30 {
			t.Fatalf("reason wrap: %q", row.Plain)
		}
	}
}

func TestAgentMessageAndApprovalPhysicalFrames(t *testing.T) {
	for _, width := range []int{24, 40, 80} {
		for _, dark := range []bool{true, false} {
			t.Run(fmt.Sprintf("%d/dark=%v", width, dark), func(t *testing.T) {
				theme := tuikit.ResolveThemeWithState(dark, false, colorprofile.TrueColor)
				ctx := BlockRenderContext{Width: width, TermWidth: width, Theme: theme}
				rows := wrapAgentMessageRows(renderAgentMessageRow("b", "reviewer[orbit]", "请验证 reconnect 测试。\nparent: 保留正文。", ctx, ""), width)
				rows = append(rows, renderACPApprovalReviewRows("b", SubagentEvent{ApprovalStatus: "denied", ApprovalText: "denied: 请确认操作范围。"}, width, ctx)...)
				// Exercise the same viewport wrapping as the document renderer, including
				// the approval header when the terminal is narrower than its title.
				model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
				block := NewParticipantTurnBlock("b", "")
				wrapped := model.wrapRenderedRowsForViewport(block, rows, width, ctx)
				styled, plain := wrapped.styledLines, wrapped.plainLines
				for i, line := range styled {
					if displayColumns(line) > width || strings.TrimRight(ansi.Strip(line), " ") != plain[i] {
						t.Fatalf("row mismatch: %q / %q", line, plain[i])
					}
				}
				height := len(styled) + 1
				before := normalizeFullscreenFrame("previous frame", width, height)
				after := normalizeFullscreenFrame(strings.Join(styled, "\n"), width, height)
				updates := renderFullscreenFramesForTest(t, width, height, before, after)
				assertPhysicalFullscreenFrame(t, width, height, after, updates)
			})
		}
	}
}
