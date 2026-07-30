package tuiapp

import (
	"strings"

	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
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

func renderSubagentOutputLifecycleRows(
	blockID string,
	event SubagentEvent,
	callID string,
	width int,
	ctx BlockRenderContext,
	final bool,
	err bool,
) []RenderedRow {
	header := terminalLifecycleHeader(event)
	token := subagentOutputOverlayClickToken(callID)
	if token != "" {
		header += "  ↗"
	}
	header = sanitizeRenderableText(header)
	args := sanitizeSpawnHeaderArgs(event.Args)
	status := subagentOutputLifecycleStatus(final, err)
	dim := status == subagentOutputRunning &&
		ctx.AnimationsEnabled &&
		subagentOutputPulseDim(ctx.SpinnerView)
	styled := renderSubagentOutputStatusMark(ctx, status) +
		" " + toolActionStyle(ctx, "Spawned").Render("Spawned")
	if args != "" {
		styled += " " + styleSpawnedHeaderDetail(ctx, args)
	}
	if token != "" {
		styled += ctx.Theme.TranscriptMetaStyle().Render("  ↗")
	}
	row := StyledPlainRow(blockID, header, styled)
	row.ClickToken = token
	row.ACPHeader = true
	row.acpHeaderMarkTone = subagentOutputHeaderMarkTone(status)
	row.acpHeaderMarkDim = dim
	return []RenderedRow{row}
}

func subagentOutputLifecycleStatus(final bool, err bool) subagentOutputStatus {
	if err {
		return subagentOutputFailed
	}
	if final {
		return subagentOutputSucceeded
	}
	return subagentOutputRunning
}

func (m *Model) subagentOutputPulseActive() bool {
	if m == nil {
		return false
	}
	if m.doc != nil {
		for _, block := range m.doc.Blocks() {
			switch typed := block.(type) {
			case *MainACPTurnBlock:
				if subagentOutputEventsRunning(typed.Events) {
					return true
				}
			case *ParticipantTurnBlock:
				if subagentOutputEventsRunning(typed.Events) {
					return true
				}
			}
		}
	}
	if m.subagentOutputOverlay != nil {
		view := m.subagentOutputViews[m.subagentOutputOverlay.callID]
		return view != nil && view.block != nil &&
			subagentOutputStatusFromState(view.block.Status) == subagentOutputRunning
	}
	return false
}

func subagentOutputEventsRunning(events []SubagentEvent) bool {
	runningByCallID := make(map[string]bool)
	unnamedRunning := false
	for _, event := range events {
		if event.Kind != SEToolCall ||
			names.CanonicalOrSelf(toolSemanticName(event.Name, event.ToolKind)) != names.Spawn {
			continue
		}
		callID := strings.TrimSpace(event.CallID)
		if callID == "" {
			unnamedRunning = unnamedRunning || !event.Done
			continue
		}
		runningByCallID[callID] = !event.Done
	}
	if unnamedRunning {
		return true
	}
	for _, running := range runningByCallID {
		if running {
			return true
		}
	}
	return false
}

func (m *Model) refreshSubagentOutputPulse() {
	if m == nil || m.doc == nil {
		return
	}
	changed := false
	for _, block := range m.doc.Blocks() {
		switch typed := block.(type) {
		case *MainACPTurnBlock:
			if subagentOutputEventsRunning(typed.Events) {
				m.markViewportBlockDirty(typed.BlockID())
				changed = true
			}
		case *ParticipantTurnBlock:
			if subagentOutputEventsRunning(typed.Events) {
				m.markViewportBlockDirty(typed.BlockID())
				changed = true
			}
		}
	}
	if changed {
		m.syncViewportContent()
	}
}
