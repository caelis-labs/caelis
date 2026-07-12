package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func renderACPLiveExplorationStageRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	if idx < 0 || idx >= len(events) || isTerminalACPTranscriptStatus(status) {
		return nil, idx, false
	}
	step, ok := collectTranscriptStep(events, idx)
	if !ok || step.start != idx || !step.allExploration {
		return nil, idx, false
	}
	if step.allDone && hasLaterTranscriptStep(events, step.end+1) {
		return nil, idx, false
	}
	stage := events[step.start : step.end+1]
	toolEvents, verb, repeatedTools := liveExplorationRepeatedToolSummary(stage)
	if !repeatedTools && !hasExplorationNarrative(stage) {
		return nil, idx, false
	}
	rows := make([]RenderedRow, 0, len(stage)+1)
	pendingTools := make([]SubagentEvent, 0, len(stage))
	for offset := 0; offset < len(stage); offset++ {
		ev := stage[offset]
		eventIdx := step.start + offset
		switch ev.Kind {
		case SEReasoning:
			if reasoningRows, consumed, ok := renderFoldableReasoningSegment(blockID, events, eventIdx, status, width, ctx, opts); ok {
				rows = append(rows, reasoningRows...)
				offset += consumed - eventIdx
			}
		case SEAssistant:
			if renderableTextHasContent(ev.Text) {
				rows = append(rows, renderParticipantTurnNarrativeEventRows(blockID, ev, tuikit.LineStyleAssistant, width, ctx, participantNarrativeEventActive(events, eventIdx, status))...)
			}
		case SEToolCall:
			if !repeatedTools && isExplorationToolEvent(ev) {
				pendingTools = append(pendingTools, ev)
			}
		}
	}
	if repeatedTools {
		if summary := liveExplorationRepeatedToolSummaryRow(blockID, toolEvents, verb, width, ctx); strings.TrimSpace(summary.Plain) != "" {
			rows = append(rows, summary)
		}
	} else {
		for _, ev := range pendingTools {
			rows = append(rows, renderLiveExplorationToolHeaderRow(blockID, ev, width, ctx))
		}
	}
	if len(rows) == 0 {
		return nil, idx, false
	}
	return rows, step.end, true
}

func renderLiveExplorationToolHeaderRow(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext) RenderedRow {
	verb := display.ExplorationVerbForTool(toolSemanticName(ev.Name, ev.ToolKind))
	if verb == "" {
		verb = names.CanonicalOrSelf(ev.Name)
	}
	detail := explorationToolDetailForDisplay(ev, ctx.Workspace, explorationToolDetailLiveSummary)
	return liveExplorationBulletHeaderRow(blockID, verb, detail, width, ctx)
}

func liveExplorationRepeatedToolSummary(stage []SubagentEvent) ([]SubagentEvent, string, bool) {
	tools := make([]SubagentEvent, 0, len(stage))
	verb := ""
	for _, ev := range stage {
		if !isExplorationToolEvent(ev) {
			continue
		}
		nextVerb := display.ExplorationVerbForTool(toolSemanticName(ev.Name, ev.ToolKind))
		if nextVerb == "" {
			return nil, "", false
		}
		if verb == "" {
			verb = nextVerb
		} else if nextVerb != verb {
			return nil, "", false
		}
		tools = append(tools, ev)
	}
	if len(tools) < 2 {
		return nil, "", false
	}
	return tools, verb, true
}

func liveExplorationRepeatedToolSummaryRow(blockID string, tools []SubagentEvent, verb string, width int, ctx BlockRenderContext) RenderedRow {
	details := make([]string, 0, len(tools))
	for _, ev := range tools {
		if detail := explorationToolDetailForDisplay(ev, ctx.Workspace, explorationToolDetailLiveSummary); detail != "" {
			details = append(details, detail)
		}
	}
	detail := strings.Join(details, ", ")
	return liveExplorationBulletHeaderRow(blockID, verb, detail, width, ctx)
}

func liveExplorationBulletHeaderRow(blockID string, verb string, detail string, width int, ctx BlockRenderContext) RenderedRow {
	plain := strings.TrimSpace("• " + strings.TrimSpace(verb))
	if detail = strings.TrimSpace(detail); detail != "" {
		plain = strings.TrimSpace(plain + " " + detail)
	}
	plain = truncateTailDisplay(plain, maxInt(16, width))
	return renderACPTranscriptHeaderRow(blockID, plain, width, ctx, "")
}
