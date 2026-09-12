package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// historicalTurnFullCount is how many newest logical turns stay fully detailed.
const historicalTurnFullCount = 2

// historicalTurnBlocks reports Block IDs that should use compact historical
// rendering. Newest historicalTurnFullCount logical turns stay full. Older
// nonterminal blocks stay full so live tools and approvals remain visible.
// User narrative, welcome, dividers, and FullAgentMessages child-pane blocks
// are omitted. Display-only: Events are not mutated.
//
// Logical turns start at a UserNarrativeBlock barrier, at the first
// MainACPTurnBlock or ParticipantTurnBlock when none is open, and at a
// timeline split where a later MainACPTurnBlock carries a different non-empty
// TurnKey. Consecutive MainACPTurnBlocks with the same or empty TurnKey stay
// one turn.
func historicalTurnBlocks(blocks []Block) map[string]bool {
	compact := make(map[string]bool)
	turns := collectHistoricalTurns(blocks)
	if len(turns) <= historicalTurnFullCount {
		return compact
	}
	for _, turn := range turns[:len(turns)-historicalTurnFullCount] {
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
}

func collectHistoricalTurns(blocks []Block) []historicalTurn {
	var turns []historicalTurn
	open := -1
	openHasMain := false
	openTurnKey := ""
	startTurn := func() {
		turns = append(turns, historicalTurn{})
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
		}
	}
	return turns
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
