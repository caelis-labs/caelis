package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// historicalTurnFullCount is how many newest restored turns stay fully
// detailed. Live Session turns are never counted and never folded.
const historicalTurnFullCount = 2

// restoringHistory reports whether the transcript events being applied
// reconstruct Session history instead of live Session output. historyBuilding
// covers the private history builders; historyReplay covers a history batch
// applied to the live transcript. Only Turns created while it reports true
// carry Block.Historical and may fold to narrative-only rendering.
func (m *Model) restoringHistory() bool {
	return m != nil && (m.historyBuilding || m.historyReplay)
}

// historicalTurnBlocks reports Block IDs that should use compact historical
// rendering. Only Turns restored from Session history replay fold: the newest
// historicalTurnFullCount restored Turns stay fully detailed and an older
// restored Turn folds only once it is terminal. A Turn created by the live
// Session is never folded just because a newer Turn began, so the execution
// trace of the Turn the user just watched stays visible. User narrative,
// welcome, dividers, and FullAgentMessages child-pane blocks are omitted.
// Display-only: Events are not mutated.
//
// Logical turns start at a UserNarrativeBlock barrier, at the first
// MainACPTurnBlock or ParticipantTurnBlock when none is open, and at a
// timeline split where a later MainACPTurnBlock carries a different non-empty
// TurnKey. Consecutive MainACPTurnBlocks with the same or empty TurnKey stay
// one turn. A logical turn counts as restored only when every contributing
// block was created by history restore.
func historicalTurnBlocks(blocks []Block) map[string]bool {
	compact := make(map[string]bool)
	turns := collectHistoricalTurns(blocks)
	restored := make([]historicalTurn, 0, len(turns))
	for _, turn := range turns {
		if turn.restored && len(turn.blocks) > 0 {
			restored = append(restored, turn)
		}
	}
	if len(restored) <= historicalTurnFullCount {
		return compact
	}
	for _, turn := range restored[:len(restored)-historicalTurnFullCount] {
		for _, block := range turn.blocks {
			if historicalTurnBlockIsTerminal(block) {
				compact[block.BlockID()] = true
			}
		}
	}
	return compact
}

// renderHistoricalTurn renders a terminal historical block as user and
// assistant narrative only. It does not mutate Events, expansion maps,
// compactHeightBudget, or exploration projection. Not for child-pane documents.
func renderHistoricalTurn(block Block, ctx BlockRenderContext) []RenderedRow {
	switch b := block.(type) {
	case *MainACPTurnBlock:
		if b == nil {
			return nil
		}
		return renderHistoricalTranscript(b.id, b.Events, b.Status, maxInt(8, ctx.Width), ctx, historicalTurnRenderOptions(b.transcriptRenderOptions()))
	case *ParticipantTurnBlock:
		if b == nil {
			return nil
		}
		return renderHistoricalTranscript(b.id, b.Events, b.Status, maxInt(8, ctx.Width), ctx, historicalTurnRenderOptions(b.transcriptRenderOptions()))
	default:
		return nil
	}
}

// historicalTurnRowHint estimates wrapped compact rows from filtered user and
// assistant narrative without markdown or tool-panel rendering.
func historicalTurnRowHint(block Block, width int) int {
	switch b := block.(type) {
	case *MainACPTurnBlock:
		if b == nil {
			return 0
		}
		return historicalTurnEventsRowHint(b.Events, width)
	case *ParticipantTurnBlock:
		if b == nil {
			return 0
		}
		return historicalTurnEventsRowHint(b.Events, width)
	default:
		return 0
	}
}

type historicalTurn struct {
	blocks []Block
	// restored is true while every contributing block was created by history
	// restore. Live Session Turns are never foldable.
	restored bool
}

func collectHistoricalTurns(blocks []Block) []historicalTurn {
	var turns []historicalTurn
	open := -1
	openHasMain := false
	openTurnKey := ""
	startTurn := func() {
		turns = append(turns, historicalTurn{restored: true})
		open = len(turns) - 1
		openHasMain = false
		openTurnKey = ""
	}
	for _, block := range blocks {
		if block == nil {
			continue
		}
		switch b := block.(type) {
		case *UserNarrativeBlock:
			startTurn()
		case *MainACPTurnBlock:
			key := strings.TrimSpace(b.TurnKey)
			if open < 0 {
				startTurn()
			} else if openHasMain && key != "" && openTurnKey != "" && key != openTurnKey {
				startTurn()
			}
			turns[open].blocks = append(turns[open].blocks, b)
			turns[open].restored = turns[open].restored && blockWasRestored(b)
			openHasMain = true
			if key != "" {
				openTurnKey = key
			}
		case *ParticipantTurnBlock:
			if open < 0 {
				startTurn()
			}
			if b.FullAgentMessages {
				continue
			}
			turns[open].blocks = append(turns[open].blocks, b)
			turns[open].restored = turns[open].restored && blockWasRestored(b)
		}
	}
	return turns
}

func blockWasRestored(block Block) bool {
	switch b := block.(type) {
	case *MainACPTurnBlock:
		return b.Historical
	case *ParticipantTurnBlock:
		return b.Historical
	default:
		return false
	}
}

func historicalTurnBlockIsTerminal(block Block) bool {
	switch b := block.(type) {
	case *MainACPTurnBlock:
		return isTerminalACPTranscriptStatus(b.Status)
	case *ParticipantTurnBlock:
		return participantTurnIsTerminal(b.Status)
	default:
		return false
	}
}

func historicalTurnRenderOptions(opts acpTranscriptRenderOptions) acpTranscriptRenderOptions {
	opts.UseStatusPlaceholder = false
	opts.StableExplorationPrep = nil
	opts.ToolPanelRows = nil
	return opts
}

func renderHistoricalTranscript(blockID string, events []SubagentEvent, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	filtered := historicalTurnEvents(events)
	if len(filtered) == 0 {
		return nil
	}
	if isTerminalACPTranscriptStatus(status) {
		status = "completed"
	}
	return renderACPTranscriptRows(blockID, filtered, status, width, ctx, opts)
}

func historicalTurnEvents(events []SubagentEvent) []SubagentEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]SubagentEvent, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case SEAssistant, SEUserInput:
			if renderableTextHasContent(ev.Text) {
				out = append(out, ev)
			}
		case SESemanticBoundary:
			out = append(out, ev)
		}
	}
	return out
}

func historicalTurnEventsRowHint(events []SubagentEvent, width int) int {
	width = maxInt(8, width)
	rows := 0
	for _, ev := range events {
		switch ev.Kind {
		case SEAssistant:
			prefix, _ := narrativeLinePrefixes(tuikit.LineStyleAssistant)
			rows += historicalNarrativeRowHint(ev.Text, prefix, width)
		case SEUserInput:
			rows += historicalNarrativeRowHint(ev.Text, userNarrativePrefix, width)
		}
	}
	return rows
}

func historicalNarrativeRowHint(text, prefix string, width int) int {
	if !renderableTextHasContent(text) {
		return 0
	}
	bodyWidth := maxInt(1, width-displayColumns(prefix))
	// An estimate must not wrap the offscreen text it is meant to defer.
	// UTF-8 bytes deliberately overestimate wide/grapheme text; demand replaces
	// this hint with the renderer's measured height before showing those rows.
	return 1 + strings.Count(text, "\n") + len(text)/bodyWidth
}
