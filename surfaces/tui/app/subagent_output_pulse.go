package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/charmbracelet/x/ansi"
)

func subagentOutputStatusFromState(status string) subagentOutputStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "success":
		return subagentOutputSucceeded
	case "failed", "cancelled", "canceled", "interrupted", "terminated", eventstream.LifecycleStateUnknown:
		return subagentOutputFailed
	default:
		return subagentOutputRunning
	}
}

func renderSubagentOutputStatusMark(ctx BlockRenderContext, status subagentOutputStatus) string {
	tone := subagentOutputHeaderMarkTone(status)
	dim := status == subagentOutputRunning &&
		ctx.AnimationsEnabled &&
		subagentOutputPulseDim(ctx.SpinnerView)
	return renderACPTranscriptHeaderMark(ctx, tone, dim)
}

func subagentOutputHeaderMarkTone(status subagentOutputStatus) acpHeaderMarkTone {
	switch status {
	case subagentOutputSucceeded:
		return acpHeaderMarkSuccess
	case subagentOutputFailed:
		return acpHeaderMarkDanger
	default:
		return acpHeaderMarkAccent
	}
}

func subagentOutputPulseDim(spinnerView string) bool {
	frame := strings.TrimSpace(ansi.Strip(spinnerView))
	for index, candidate := range runningSpinnerFrames {
		if frame == candidate {
			return index >= len(runningSpinnerFrames)/2
		}
	}
	return false
}

func (m *Model) subagentOutputPulseActive() bool {
	if m == nil {
		return false
	}
	if m.subagentOutputOverlay != nil {
		view := m.subagentOutputViews[m.subagentOutputOverlay.callID]
		if view != nil && view.block != nil &&
			subagentOutputStatusFromState(view.block.Status) == subagentOutputRunning {
			return true
		}
	}
	return m.subagentRosterHasRunning()
}
