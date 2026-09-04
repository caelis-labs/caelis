package tuiapp

import (
	"strings"
	"testing"
)

func TestCompactSingleLineBudgetUsesAvailableWidthWithCap(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		width int
		want  int
	}{
		{width: 64, want: 52},
		{width: 80, want: 68},
		{width: 120, want: 108},
		{width: 160, want: 132},
		{width: 240, want: 132},
	} {
		if got := compactSingleLineBudget(testCase.width); got != testCase.want {
			t.Errorf("compactSingleLineBudget(%d) = %d, want %d", testCase.width, got, testCase.want)
		}
	}
}

func TestCompactToolHeadersUseAdaptiveWidth(t *testing.T) {
	t.Parallel()

	genericArgs := strings.Repeat("path-segment ", 20)
	narrowGeneric := compactACPToolHeaderEvent(SubagentEvent{Name: "Read", Args: genericArgs}, 80)
	wideGeneric := compactACPToolHeaderEvent(SubagentEvent{Name: "Read", Args: genericArgs}, 160)
	if got := displayColumns(narrowGeneric.Args); got > compactSingleLineBudget(80) {
		t.Fatalf("narrow generic args width = %d, want <= %d", got, compactSingleLineBudget(80))
	}
	if got := displayColumns(wideGeneric.Args); got <= 72 || got > compactSingleLineBudget(160) {
		t.Fatalf("wide generic args width = %d, want in (72, %d]", got, compactSingleLineBudget(160))
	}

	command := "python3 -c " + strings.Repeat(`print("adaptive width");`, 12)
	event := SubagentEvent{
		Name:     surfaceToolRunCommand,
		ToolKind: "execute",
		Args:     truncateDisplayPreviewMiddle(command, toolArgsPreviewWidth),
		FullArgs: command,
	}
	narrowCommand := compactACPToolHeaderEvent(event, 80)
	wideCommand := compactACPToolHeaderEvent(event, 160)
	if got := displayColumns(narrowCommand.Args); got > compactSingleLineBudget(80) {
		t.Fatalf("narrow command args width = %d, want <= %d", got, compactSingleLineBudget(80))
	}
	if got := displayColumns(wideCommand.Args); got <= toolArgsPreviewWidth || got > compactSingleLineBudget(160) {
		t.Fatalf("wide command args width = %d, want in (%d, %d]", got, toolArgsPreviewWidth, compactSingleLineBudget(160))
	}
}

func TestProjectedActivityHeadersStayWithinViewport(t *testing.T) {
	t.Parallel()

	const width = 120
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	ctx := model.blockRenderContext(width)
	fullArgs := strings.Repeat("long-activity-segment ", 16)
	preview := truncateDisplayPreviewMiddle(fullArgs, toolArgsPreviewWidth)

	generic := renderACPStandardToolCollapsedRows("block", SubagentEvent{
		Name: "mcp_server_with_a_deliberately_long_tool_name", Args: fullArgs,
	}, "generic", width, ctx, false, true, "")
	ran := renderACPTerminalLifecycleRows("block", SubagentEvent{
		Name: surfaceToolRunCommand, ToolKind: "execute", Args: preview, FullArgs: fullArgs,
	}, "ran", "", width, ctx, false, false, true, false, acpTranscriptRenderOptions{})
	spawned := renderACPSpawnToolRows("block", SubagentEvent{
		Name: surfaceToolSpawn, Args: preview, FullArgs: fullArgs,
	}, "spawned", width, ctx)
	sent := renderACPTerminalLifecycleRows("block", SubagentEvent{
		Name: surfaceToolSendMessage, Args: preview, FullArgs: fullArgs,
	}, "sent", "", width, ctx, false, false, true, false, acpTranscriptRenderOptions{})
	received := renderAgentCommunicationRows("block", SubagentEvent{
		Kind: SEToolCall, SourceName: strings.Repeat("reviewer", 8), Text: fullArgs,
	}, width, ctx, acpTranscriptRenderOptions{})

	for name, rows := range map[string][]RenderedRow{
		"generic":  generic,
		"ran":      ran,
		"spawned":  spawned,
		"sent":     sent,
		"received": received,
	} {
		if len(rows) != 1 {
			t.Errorf("%s rows = %d, want 1", name, len(rows))
			continue
		}
		if got := displayColumns(rows[0].Plain); got > width {
			t.Errorf("%s header width = %d, want <= %d: %q", name, got, width, rows[0].Plain)
		}
		if strings.Contains(rows[0].Plain, "\n") {
			t.Errorf("%s header contains newline: %q", name, rows[0].Plain)
		}
	}
}

func TestReceivedAgentCommunicationUsesAdaptiveWidth(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	event := SubagentEvent{
		Kind:       SEAgentCommunication,
		SourceName: "reviewer",
		Text:       strings.Repeat("message-segment ", 20),
	}
	narrow := renderAgentCommunicationRows("block", event, 80, model.blockRenderContext(80), acpTranscriptRenderOptions{})
	wide := renderAgentCommunicationRows("block", event, 160, model.blockRenderContext(160), acpTranscriptRenderOptions{})
	if len(narrow) != 1 || len(wide) != 1 {
		t.Fatalf("adaptive Received rows = %d narrow, %d wide; want one each", len(narrow), len(wide))
	}
	narrowDetail := strings.TrimPrefix(narrow[0].Plain, "• Received ")
	wideDetail := strings.TrimPrefix(wide[0].Plain, "• Received ")
	if got := displayColumns(narrowDetail); got > compactSingleLineBudget(80) {
		t.Fatalf("narrow Received detail width = %d, want <= %d", got, compactSingleLineBudget(80))
	}
	if got := displayColumns(wideDetail); got <= toolArgsPreviewWidth || got > compactSingleLineBudget(160) {
		t.Fatalf("wide Received detail width = %d, want in (%d, %d]", got, toolArgsPreviewWidth, compactSingleLineBudget(160))
	}
}
