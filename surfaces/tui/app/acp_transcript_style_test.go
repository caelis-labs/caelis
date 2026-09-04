package tuiapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestACPToolOutputSanitizesANSIAndKeepsStructuralPrefix(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 64, TermWidth: 64, Theme: model.theme}

	rows := renderACPToolOutputRowsWithToken(
		"block-1",
		"  └ ",
		"\x1b[31m└ failed\x1b[0m\n\x1b[32m+line\x1b[0m",
		64,
		ctx,
		ctx.Theme.ToolOutputStyle(),
		"",
	)

	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %#v", len(rows), rows)
	}
	wantPlain := []string{"  └ └ failed", "    +line"}
	for i, row := range rows {
		if row.Plain != wantPlain[i] {
			t.Fatalf("row %d plain = %q, want %q", i, row.Plain, wantPlain[i])
		}
		if got := strings.TrimRight(ansi.Strip(row.Styled), " "); got != row.Plain {
			t.Fatalf("row %d styled strips to %q, want %q", i, got, row.Plain)
		}
		if strings.Contains(row.Styled, "[31m") || strings.Contains(row.Styled, "[32m") {
			t.Fatalf("row %d leaked source ANSI color into styled output: %q", i, row.Styled)
		}
	}
	metaPrefix := ctx.Theme.TranscriptMetaStyle().Render("  └ ")
	if !strings.HasPrefix(rows[0].Styled, metaPrefix) {
		t.Fatalf("structural prefix should use transcript meta style\nprefix=%q\nstyled=%q", metaPrefix, rows[0].Styled)
	}
}

func TestStyleTerminalOutputLineSanitizesANSI(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 64, TermWidth: 64, Theme: model.theme}

	styled := styleTerminalOutputLine(ctx, "  └ ", "\x1b[31mfailed\x1b[0m", ctx.Theme.ToolErrorStyle())
	if got := strings.TrimRight(ansi.Strip(styled), " "); got != "  └ failed" {
		t.Fatalf("styled strips to %q, want structural prefix plus sanitized content", got)
	}
	if strings.Contains(styled, "[31m") {
		t.Fatalf("source ANSI color leaked into terminal output line: %q", styled)
	}
	metaPrefix := ctx.Theme.TranscriptMetaStyle().Render("  └ ")
	if !strings.HasPrefix(styled, metaPrefix) {
		t.Fatalf("structural prefix should use transcript meta style\nprefix=%q\nstyled=%q", metaPrefix, styled)
	}
}

func TestTerminalOutputKeepsNonBlankLineWhitespaceOnlyDropsBlankLines(t *testing.T) {
	t.Parallel()

	segments := tailWrappedTerminalSegments("  first line  \r\n   \r\nsecond line  \r\n", 80, 10)
	want := []string{"  first line  ", "second line  "}
	if strings.Join(segments, "\n") != strings.Join(want, "\n") {
		t.Fatalf("segments = %#v, want %#v", segments, want)
	}
}

func TestACPHeaderSanitizesSourceANSI(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme}

	header := styleACPTranscriptHeader(ctx, "• Ran \x1b[31mgit\x1b[0m status --short")
	if got := ansi.Strip(header); got != "• Ran git status --short" {
		t.Fatalf("header strips to %q, want sanitized shell command", got)
	}
	if strings.Contains(header, "[31m") {
		t.Fatalf("source ANSI color leaked into header: %q", header)
	}
}

func TestExplorationSummaryWrappedDetailStylesContinuationNumbers(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 58, TermWidth: 58, Theme: model.theme}
	rows := wrapExplorationSummaryDetail("  └ ", "Read", "common.go 1~200, common.go 201~400, ebs_snapshot.go 901~1100", 58)
	if len(rows) < 2 {
		t.Fatalf("rows = %#v, want wrapped continuation", rows)
	}
	styled := styleExplorationSummaryRow(rows[1], ctx)
	if got := strings.TrimRight(ansi.Strip(styled), " "); got != rows[1] {
		t.Fatalf("styled strips to %q, want %q", got, rows[1])
	}
	numberFG := sgrForegroundCode(t, model.theme.TextStyle().GetForeground())
	numberText := normalizeInlineStyleText(textWithSGRForeground(styled, numberFG))
	if !strings.Contains(numberText, "901") || !strings.Contains(numberText, "1100") {
		t.Fatalf("continuation numbers not styled with number foreground\nnumbers=%q\nstyled=%q", numberText, styled)
	}
}

func TestExplorationSummaryStylesViewAsToolAction(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 80, TermWidth: 80, Theme: model.theme}
	block := NewMainACPTurnBlock("turn-view")
	block.UpdateToolWithMeta("view-1", "ViewImage", "page-4.png", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("read-1", "Read", "notes.txt", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.Status = "completed"

	var viewRow RenderedRow
	for _, row := range block.Render(ctx) {
		if strings.Contains(row.Plain, "View page-4.png") {
			viewRow = row
			break
		}
	}
	if viewRow.Plain == "" {
		t.Fatal("Explored summary omitted ViewImage row")
	}
	if got := strings.TrimRight(ansi.Strip(viewRow.Styled), " "); got != viewRow.Plain {
		t.Fatalf("styled strips to %q, want %q", got, viewRow.Plain)
	}
	actionText := ansiTextForForeground(t, viewRow.Styled, model.theme.ToolActionStyle(tuikit.ToolActionNeutral).GetForeground())
	if actionText != "View" {
		t.Fatalf("tool action foreground covers %q, want View only\nstyled=%q", actionText, viewRow.Styled)
	}
}

func TestExplorationSummaryStylesNormalizedListLikeStandaloneToolHeader(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 80, TermWidth: 80, Theme: model.theme}
	block := NewMainACPTurnBlock("turn-list")
	block.UpdateToolWithMeta("list-1", "", "docs", "", true, false, ToolUpdateMeta{
		ToolKind: eventstream.ToolKindOther, ToolTitle: "List `docs`", ExplorationVerb: "List",
	})
	block.UpdateToolWithMeta("read-1", "", "go.mod", "", true, false, ToolUpdateMeta{ToolKind: eventstream.ToolKindRead})
	block.Status = eventstream.ToolStatusCompleted

	var listRow RenderedRow
	for _, row := range block.Render(ctx) {
		if strings.Contains(row.Plain, "List docs") {
			listRow = row
			break
		}
	}
	if listRow.Plain == "" {
		t.Fatal("Explored summary omitted normalized List row")
	}
	if got := strings.TrimRight(ansi.Strip(listRow.Styled), " "); got != listRow.Plain {
		t.Fatalf("styled strips to %q, want %q", got, listRow.Plain)
	}
	actionText := ansiTextForForeground(t, listRow.Styled, model.theme.ToolActionStyle(tuikit.ToolActionNeutral).GetForeground())
	if actionText != "List" {
		t.Fatalf("tool action foreground covers %q, want List only\nstyled=%q", actionText, listRow.Styled)
	}
	argsText := normalizeInlineStyleText(ansiTextForForeground(t, listRow.Styled, model.theme.ToolArgsStyle().GetForeground()))
	if !strings.Contains(argsText, "docs") || strings.Contains(argsText, "List") {
		t.Fatalf("tool args foreground covers %q, want docs without List\nstyled=%q", argsText, listRow.Styled)
	}
}

func TestACPModelRetryNoticeStylesTextAndNumbersSeparately(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme}
	rows := renderACPNoticeRows("block-1", SubagentEvent{
		Kind:       SENotice,
		Text:       "Retrying model request (12/50, retry in 10s)",
		NoticeKind: transcript.NoticeKindModelRetry,
	}, 96, ctx)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want one retry notice row", rows)
	}
	if rows[0].Plain != "! Retrying model request (12/50, retry in 10s)" {
		t.Fatalf("plain retry notice = %q", rows[0].Plain)
	}
	if got := ansi.Strip(rows[0].Styled); got != rows[0].Plain {
		t.Fatalf("styled strips to %q, want %q", got, rows[0].Plain)
	}
	numberFG := sgrForegroundCode(t, model.theme.TextStyle().GetForeground())
	numberText := normalizeInlineStyleText(textWithSGRForeground(rows[0].Styled, numberFG))
	if !strings.Contains(numberText, "12") || !strings.Contains(numberText, "50") || !strings.Contains(numberText, "10") ||
		strings.Count(numberText, "12") != 1 || strings.Count(numberText, "50") != 1 || strings.Count(numberText, "10") != 1 {
		t.Fatalf("retry notice numbers = %q, want high-contrast number spans", numberText)
	}
	metaFG := sgrForegroundCode(t, model.theme.TranscriptMetaStyle().GetForeground())
	metaText := normalizeInlineStyleText(textWithSGRForeground(rows[0].Styled, metaFG))
	if !strings.Contains(metaText, "Retrying model request") || strings.Contains(metaText, "12") || strings.Contains(metaText, "50") || strings.Contains(metaText, "10") {
		t.Fatalf("retry notice meta text = %q, want low-contrast text without numbers", metaText)
	}
}

func TestACPCompactNoticeUsesCompactedLabelAndMetaStyle(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme}
	text := "• " + transcript.CompactNoticeLabel
	rows := renderACPNoticeRows("block-1", SubagentEvent{
		Kind:       SENotice,
		Text:       text,
		NoticeKind: transcript.NoticeKindCompact,
	}, 96, ctx)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want one compact notice row", rows)
	}
	if rows[0].Plain != "• Context compacted" {
		t.Fatalf("plain compact notice = %q", rows[0].Plain)
	}
	metaFG := sgrForegroundCode(t, model.theme.TranscriptMetaStyle().GetForeground())
	metaText := normalizeInlineStyleText(textWithSGRForeground(rows[0].Styled, metaFG))
	if !strings.Contains(metaText, "Context compacted") {
		t.Fatalf("compact notice meta text = %q, styled=%q", metaText, rows[0].Styled)
	}
}

func TestACPInterruptedTurnNoticeUsesNeutralHierarchyAndOneLeadingBlankLine(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme}
	rows := renderACPTranscriptRows("block-1", []SubagentEvent{{
		Kind: SEAssistant,
		Text: "Partial response before the stop.",
	}}, eventstream.LifecycleStateInterrupted, 96, ctx, acpTranscriptRenderOptions{})

	noticeIndex := -1
	for i, row := range rows {
		if row.Plain == interruptedTurnTitle {
			noticeIndex = i
			break
		}
	}
	if noticeIndex < 2 {
		t.Fatalf("interruption title index = %d, rows = %#v", noticeIndex, renderedPlainRows(rows))
	}
	if strings.TrimSpace(rows[noticeIndex-1].Plain) != "" || strings.TrimSpace(rows[noticeIndex-2].Plain) == "" {
		t.Fatalf("interruption notice does not have exactly one blank row before it: %#v", renderedPlainRows(rows))
	}

	want := []string{interruptedTurnTitle, interruptedTurnCause, interruptedTurnNext}
	if noticeIndex+len(want) > len(rows) {
		t.Fatalf("interruption rows = %#v, want %q", renderedPlainRows(rows), want)
	}
	for i, text := range want {
		row := rows[noticeIndex+i]
		if row.Plain != text {
			t.Fatalf("interruption row %d = %q, want %q", i, row.Plain, text)
		}
		style := ctx.Theme.HelpHintTextStyle()
		if i == 0 {
			style = ctx.Theme.TextStyle()
		}
		if row.Styled != style.Render(text) {
			t.Fatalf("interruption row %d uses non-neutral styling: %q", i, row.Styled)
		}
		if !row.PreWrapped {
			t.Fatalf("interruption row %d is not marked pre-wrapped", i)
		}
	}
	if plain := strings.Join(renderedPlainRows(rows), "\n"); strings.Contains(plain, "⊘") {
		t.Fatalf("interruption notice retained warning icon:\n%s", plain)
	}
}

func TestACPInterruptedTurnNoticeWordWrapsAtNarrowWidths(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 32, TermWidth: 32, Theme: model.theme}
	rows := renderACPStatusRows("block-1", eventstream.LifecycleStateCancelled, 32, ctx, acpTranscriptRenderOptions{})
	want := []string{
		"Turn interrupted",
		"Stopped by an interrupt command",
		"— this is not a system error.",
		"Send another message to continue",
		"or change direction.",
	}
	if got := renderedPlainRows(rows); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("narrow interruption notice = %#v, want %#v", got, want)
	}
	for i, row := range rows {
		if displayColumns(row.Plain) > 32 {
			t.Fatalf("interruption row %d width = %d, want at most 32: %q", i, displayColumns(row.Plain), row.Plain)
		}
	}
}

func TestTerminalErrorLineRedactsModelRetryDetails(t *testing.T) {
	line := terminalErrorLine(&model.RetryExhaustedError{
		MaxRetries: 5,
		Cause:      errors.New("model: http status 500 body=Internal Server Error"),
	})
	if line != "✗ model request failed after 5 retries" {
		t.Fatalf("terminalErrorLine() = %q, want redacted retry failure", line)
	}
	if strings.Contains(line, "Internal Server Error") || strings.Contains(line, "http status 500") {
		t.Fatalf("terminal error leaked provider detail: %q", line)
	}
}

func TestTerminalLifecycleForTaskResultRedactsModelRetryDetails(t *testing.T) {
	env := terminalLifecycleForTaskResult(TaskResultMsg{
		Err: &model.RetryExhaustedError{
			MaxRetries: 5,
			Cause:      errors.New("model: http status 500 body=Internal Server Error"),
		},
	}, time.Unix(120, 0))
	if env.Lifecycle == nil || env.Lifecycle.Reason != "model request failed after 5 retries" {
		t.Fatalf("terminal lifecycle = %#v, want redacted retry reason", env.Lifecycle)
	}
	line := terminalErrorLine(errorFromTerminalLifecycle(env))
	if line != "✗ model request failed after 5 retries" {
		t.Fatalf("terminalErrorLine(errorFromTerminalLifecycle()) = %q, want redacted retry failure", line)
	}
	if strings.Contains(line, "Internal Server Error") || strings.Contains(line, "http status 500") ||
		strings.Contains(env.Lifecycle.Reason, "Internal Server Error") || strings.Contains(env.Lifecycle.Reason, "http status 500") {
		t.Fatalf("terminal lifecycle leaked provider detail: reason=%q line=%q", env.Lifecycle.Reason, line)
	}
}

func TestACPToolHeaderMarkFailsRedAndPulsesWhileRunning(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	ctx := BlockRenderContext{Width: 80, TermWidth: 80, Theme: theme, AnimationsEnabled: true}

	success := renderACPToolHeaderRow("b", "• Ran git status", 80, ctx, "", false, true)
	if got := ansiTextForForeground(t, success.Styled, theme.ToolFg); got != "•" {
		t.Fatalf("success mark = %q, want tool color\nstyled=%q", got, success.Styled)
	}
	if got := ansiTextForForeground(t, success.Styled, theme.Error); strings.Contains(got, "•") {
		t.Fatalf("success mark should stay off the error color, got %q", got)
	}

	failed := renderACPToolHeaderRow("b", "• Ran git status", 80, ctx, "", true, true)
	if got := ansiTextForForeground(t, failed.Styled, theme.Error); !strings.Contains(got, "•") {
		t.Fatalf("failed mark = %q, want error color\nstyled=%q", got, failed.Styled)
	}

	ctx.SpinnerView = runningSpinnerFrames[0]
	bright := renderACPToolHeaderRow("b", "• Ran git status", 80, ctx, "", false, false)
	ctx.SpinnerView = runningSpinnerFrames[len(runningSpinnerFrames)/2]
	dim := renderACPToolHeaderRow("b", "• Ran git status", 80, ctx, "", false, false)
	if bright.Styled == dim.Styled {
		t.Fatal("running header mark did not breathe")
	}
	if bright.Plain != dim.Plain {
		t.Fatalf("pulse changed plain text: %q vs %q", bright.Plain, dim.Plain)
	}

	ctx.SpinnerView = runningSpinnerFrames[0]
	completedBright := renderACPToolHeaderRow("b", "• Ran git status", 80, ctx, "", false, true)
	ctx.SpinnerView = runningSpinnerFrames[len(runningSpinnerFrames)/2]
	completedDim := renderACPToolHeaderRow("b", "• Ran git status", 80, ctx, "", false, true)
	if completedBright.Styled != completedDim.Styled {
		t.Fatal("completed header still pulses")
	}
}

func TestViewportCacheKeyIncludesPulseOnlyForRunningTools(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	block := NewMainACPTurnBlock("turn")
	block.Events = []SubagentEvent{{
		Kind: SEToolCall, Name: "RunCommand", Args: "git status", Done: false,
	}}
	bright := BlockRenderContext{Width: 80, TermWidth: 80, Theme: theme, AnimationsEnabled: true, SpinnerView: runningSpinnerFrames[0]}
	dim := bright
	dim.SpinnerView = runningSpinnerFrames[len(runningSpinnerFrames)/2]
	if viewportBlockRenderKey(block, bright) == viewportBlockRenderKey(block, dim) {
		t.Fatal("running tool cache key ignored pulse phase")
	}
	block.Events[0].Done = true
	if viewportBlockRenderKey(block, bright) != viewportBlockRenderKey(block, dim) {
		t.Fatal("completed tool cache key changed with pulse phase")
	}
	block.Events[0].Name = "Spawn"
	block.Events[0].Done = false
	if viewportBlockRenderKey(block, bright) != viewportBlockRenderKey(block, dim) {
		t.Fatal("Spawn row cache key should stay independent of pulse phase")
	}
}

func TestViewportCacheKeyIncludesStandardACPToolTitle(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn")
	block.Events = []SubagentEvent{{
		Kind: SEToolCall, CallID: "other-1", ToolKind: "other", Title: "Start subagent reviewer", Done: true,
	}}
	ctx := BlockRenderContext{Width: 80, TermWidth: 80}
	before := viewportBlockRenderKey(block, ctx)
	block.Events[0].Title = "Start subagent explorer"
	if after := viewportBlockRenderKey(block, ctx); before == after {
		t.Fatal("standard ACP title change did not invalidate the viewport render cache")
	}
}

func TestCommandTaskWriteKeepsInteractionHeaderWhenTerminalTagged(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	ctx := BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme}
	ev := SubagentEvent{
		Kind:           SEToolCall,
		Name:           "Task",
		Args:           "Write command-82",
		CallID:         "task-write-82",
		ToolKind:       "other",
		TaskAction:     "write",
		TaskInput:      "你好，确认一下工作目录和模型，一句话回答",
		TaskHandle:     "command-82",
		TaskTargetKind: "command",
		Terminal:       true,
		Done:           true,
	}
	rows, _ := renderACPToolLifecycleRows("b", []SubagentEvent{ev}, 0, 120, ctx, acpTranscriptRenderOptions{ToolOutputPanels: true})
	var plain []string
	for _, row := range rows {
		plain = append(plain, row.Plain)
	}
	got := strings.Join(plain, "\n")
	if !strings.Contains(got, "• Interacted with background shell: 你好，确认一下工作目录和模型，一句话回答") {
		t.Fatalf("command Task write header = %q, want background-shell interaction", got)
	}
	if strings.Contains(got, "Ran Task Write") || strings.Contains(got, "command-82") {
		t.Fatalf("command Task write leaked the execute/Ran header or handle:\n%s", got)
	}
}

func TestMutationLifecycleHeaderUsesEdit(t *testing.T) {
	t.Parallel()

	write := SubagentEvent{Name: "Write", Args: "process_alive.go +21 -0"}
	if got := mutationLifecycleHeader(write, false); got != "• Edit process_alive.go +21 -0" {
		t.Fatalf("write header = %q, want Edit", got)
	}
	patch := SubagentEvent{Name: "Patch", Args: "theme.go +3 -1"}
	if got := mutationLifecycleHeader(patch, false); got != "• Edit theme.go +3 -1" {
		t.Fatalf("patch header = %q, want Edit", got)
	}
	if got := mutationLifecycleHeader(write, true); got != "• Edit process_alive.go +21 -0 failed" {
		t.Fatalf("failed write header = %q, want Edit ... failed", got)
	}
}
