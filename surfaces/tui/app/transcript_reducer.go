package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

type transcriptToolMutation struct {
	callID string
	name   string
	args   string
	stream string
	output string
	final  bool
	err    bool
	meta   ToolUpdateMeta
}

func transcriptToolMutationFromEvent(event TranscriptEvent) transcriptToolMutation {
	return transcriptToolMutation{
		callID: event.ToolCallID,
		name:   event.ToolName,
		args:   event.ToolArgs,
		stream: firstNonEmpty(strings.TrimSpace(event.ToolStream), "stdout"),
		output: event.ToolOutput,
		final:  event.Final,
		err:    event.ToolError,
		meta:   transcriptToolUpdateMeta(event),
	}
}

func (m *Model) applyTranscriptToolToParticipant(event TranscriptEvent, mutation transcriptToolMutation) (tea.Model, tea.Cmd) {
	block := m.ensureParticipantTurnBlock(transcriptParticipantTurnKey(event), event.Actor)
	if block == nil {
		return m, nil
	}
	if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
		block.StartedAt = event.OccurredAt
	}
	if state := strings.ToLower(strings.TrimSpace(block.Status)); state == "initializing" || state == "prompting" {
		block.Status = "running"
	}
	block.UpdateToolWithMeta(mutation.callID, mutation.name, mutation.args, mutation.output, mutation.final, mutation.err, mutation.meta)
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) applyTranscriptToolToSubagent(event TranscriptEvent, mutation transcriptToolMutation) (tea.Model, tea.Cmd) {
	if eventAnchorsSpawnSubagentTool(event) {
		return m, nil
	}
	return m.applyTranscriptToolToParticipant(event, mutation)
}

func (m *Model) applyTranscriptToolToMain(event TranscriptEvent, mutation transcriptToolMutation) (tea.Model, tea.Cmd) {
	block := m.ensureMainACPTurnBlock(strings.TrimSpace(event.ScopeID))
	if block == nil {
		return m, nil
	}
	if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
		block.StartedAt = event.OccurredAt
	}
	block.UpdateToolWithMeta(mutation.callID, mutation.name, mutation.args, mutation.output, mutation.final, mutation.err, mutation.meta)
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}
