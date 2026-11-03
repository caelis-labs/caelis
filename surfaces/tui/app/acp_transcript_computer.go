package tuiapp

import (
	"fmt"
	"strings"
)

func isComputerUseToolEvent(ev SubagentEvent) bool {
	if ev.Kind != SEToolCall {
		return false
	}
	// Only canonical projected DTW identities qualify. The mcp_call envelope,
	// arbitrary suffixes and standard ACP kinds are not computer-use identity.
	switch strings.TrimSpace(ev.Name) {
	case "desktop_world__apps", "desktop_world__exec":
		return true
	case "desktop_world__api", "desktop_world__result":
		return true // Read-only presentation of older Session history.
	default:
		return false
	}
}

func isGroupedToolEvent(ev SubagentEvent) bool {
	return isExplorationToolEvent(ev) || (isComputerUseToolEvent(ev) && strings.TrimSpace(ev.CallID) != "" && ev.ApprovalStatus == "")
}

// Computer use shares the existing stable run, replay and expansion owner.
// Its children retain ordinary tool disclosure and their own click targets.
func renderComputerUseContainerRows(blockID string, events []SubagentEvent, end int, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions, container *explorationContainerState) ([]RenderedRow, int, bool) {
	token := acpStableExplorationClickToken(container.StableID)
	header := "• Using Computer"
	failed := 0
	names := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Kind != SEToolCall {
			continue
		}
		if ev.Err {
			failed++
		}
		if !seen[ev.Name] {
			names = append(names, ev.Name)
			seen[ev.Name] = true
		}
	}
	if failed > 0 {
		header += fmt.Sprintf(" · %d failed", failed)
	}
	tone, dim := acpToolHeaderMark(ctx, failed > 0, true)
	rows := []RenderedRow{renderACPTranscriptHeaderRowMarked(blockID, header, width, ctx, token, tone, dim)}
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(container.StableID)
	if !expanded {
		summary := fmt.Sprintf("%d calls · %s", len(container.CallIDs), strings.Join(names, ", "))
		rows = append(rows, renderACPToolDetailRowsWithToken(blockID, "  └ ", summary, width, ctx, ctx.Theme.ToolOutputMetaStyle(), token)...)
		return rows, end, true
	}
	for i := 0; i < len(events); i++ {
		if events[i].Kind == SEReasoning {
			rows = append(rows, renderExplorationNarrativeRows(blockID, events[i].Text, width, ctx, ctx.Theme.ReasoningStyle(), token, len(rows) == 1)...)
			continue
		}
		if events[i].Kind != SEToolCall {
			continue
		}
		children, consumed := renderACPToolLifecycleRowsWithoutReview(blockID, events, i, width, ctx, opts)
		rows = append(rows, children...)
		i = consumed
	}
	return rows, end, true
}
