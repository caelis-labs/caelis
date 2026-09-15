package tuiapp

import "strings"

// This recognizes only the presentation fold label, not tool status or content.
func isToolOutputFoldMarker(text string) bool {
	text = strings.TrimSpace(text)
	count, ok := strings.CutPrefix(text, "... +")
	if !ok {
		return false
	}
	count, ok = strings.CutSuffix(count, " lines")
	if !ok || count == "" {
		return false
	}
	for _, digit := range count {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func toolPanelLiveTail(callID string, text string, width int, ctx BlockRenderContext, final, fullOutput, failed bool, opts acpTranscriptRenderOptions) bool {
	if final || fullOutput || failed || !ctx.AnimationsEnabled {
		return false
	}
	if opts.ToolPanelScrollState != nil && !opts.ToolPanelScrollState(callID).FollowTail {
		return false
	}
	// A short output has not started scrolling. One displayed row beyond
	// the preview window is enough to establish overflow.
	return len(tailWrappedTerminalSegmentsFromEnd(text, width, acpTerminalPanelMaxLines+1)) > acpTerminalPanelMaxLines
}
