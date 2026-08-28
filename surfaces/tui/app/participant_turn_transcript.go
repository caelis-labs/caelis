package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type participantTurnTranscriptPolicy struct {
	actor                    string
	appendFinalNarrative     bool
	completeOnFinalAssistant bool
	hideTaskControl          bool
	monotonicStatus          bool
	reopenPlan               bool
}

type participantTurnTranscriptResult struct {
	changed  bool
	terminal bool
}

func (m *Model) applyTranscriptNarrativeToParticipantTurn(event TranscriptEvent) (tea.Model, tea.Cmd) {
	text := tuikit.SanitizeLogText(transcriptNarrativeText(event))
	if text == "" && !event.Final {
		return m, nil
	}
	block := m.ensureParticipantTurnBlock(transcriptParticipantTurnKey(event), participantTurnTranscriptActor(event))
	if block == nil {
		return m, nil
	}
	m.activeParticipantTurnSessionID = strings.TrimSpace(block.SessionID)
	if !block.EndedAt.IsZero() {
		block.EndedAt = time.Time{}
	}
	result := applyTranscriptEventToParticipantTurn(block, event, participantTurnTranscriptPolicy{
		actor:                    participantTurnTranscriptActor(event),
		completeOnFinalAssistant: true,
	})
	if !result.changed {
		return m, nil
	}
	if event.Final && event.NarrativeKind == TranscriptNarrativeAssistant {
		m.activeParticipantTurnSessionID = ""
	}
	m.markViewportBlockDirty(block.BlockID())
	m.hasCommittedLine = true
	m.lastCommittedStyle = tuikit.LineStyleAssistant
	m.lastCommittedRaw = strings.TrimSpace(block.Actor) + ":"
	return m, m.requestStreamViewportSync()
}

// applyTranscriptEventToParticipantTurn is the shared presentation reducer for
// document-backed Side ACP turns and detached subagent output views. Callers
// retain ownership of block lookup, viewport invalidation, and stream demand.
func applyTranscriptEventToParticipantTurn(
	block *ParticipantTurnBlock,
	event TranscriptEvent,
	policy participantTurnTranscriptPolicy,
) participantTurnTranscriptResult {
	if block == nil {
		return participantTurnTranscriptResult{}
	}
	if actor := strings.TrimSpace(policy.actor); actor != "" {
		block.Actor = participantActorDisplayName(actor)
	}
	if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
		block.StartedAt = event.OccurredAt
	}

	switch event.Kind {
	case TranscriptEventNarrative:
		return applyTranscriptNarrativeToParticipantTurn(block, event, policy)
	case TranscriptEventNotice:
		block.AddNotice(formatTranscriptNoticeText(event.Text), event.OccurredAt, event.NoticeKind)
	case TranscriptEventAgentCommunication:
		block.AddAgentCommunication(agentCommunicationSubagentEvent(event))
	case TranscriptEventPlan:
		state := strings.ToLower(strings.TrimSpace(block.Status))
		if state == "initializing" || state == "prompting" ||
			(policy.reopenPlan &&
				(state == "waiting_approval" || (!policy.monotonicStatus && participantTurnIsTerminal(state)))) {
			block.Status = "running"
		}
		block.UpdatePlan(transcriptPlanEntries(event))
	case TranscriptEventTool:
		if _, hidden := hiddenTaskControlAction(event); hidden && policy.hideTaskControl {
			block.advanceNarrativeBoundaryWithGap()
			break
		}
		mutation := transcriptToolMutationFromEvent(event)
		if state := strings.ToLower(strings.TrimSpace(block.Status)); state == "initializing" || state == "prompting" {
			block.Status = "running"
		}
		block.UpdateToolWithMeta(
			mutation.callID,
			mutation.name,
			mutation.args,
			mutation.output,
			mutation.final,
			mutation.err,
			mutation.meta,
		)
	case TranscriptEventApproval:
		if strings.TrimSpace(event.ApprovalText) != "" {
			if strings.EqualFold(strings.TrimSpace(block.Status), "waiting_approval") {
				block.Status = "running"
			}
			block.AddApprovalReviewEvent(
				event.ToolCallID,
				event.ApprovalTool,
				event.ApprovalCommand,
				event.ApprovalStatus,
				event.ApprovalRisk,
				event.ApprovalAuth,
				event.ApprovalText,
			)
			break
		}
		setParticipantTurnTranscriptStatus(
			block,
			firstNonEmpty(strings.TrimSpace(event.State), "waiting_approval"),
			event.ApprovalTool,
			event.ApprovalCommand,
			event,
			policy.monotonicStatus,
		)
	case TranscriptEventParticipant, TranscriptEventLifecycle:
		setParticipantTurnTranscriptStatus(block, event.State, "", "", event, policy.monotonicStatus)
	case transcript.EventError:
		block.AddNotice(event.Text, event.OccurredAt, event.NoticeKind)
	default:
		return participantTurnTranscriptResult{}
	}
	return participantTurnTranscriptResult{
		changed:  true,
		terminal: eventstream.IsTerminalLifecycleState(block.Status),
	}
}

func transcriptPlanEntries(event TranscriptEvent) []planEntryState {
	entries := make([]planEntryState, 0, len(event.PlanEntries))
	for _, entry := range event.PlanEntries {
		entries = append(entries, planEntryState{Content: entry.Content, Status: entry.Status})
	}
	return entries
}

func applyTranscriptNarrativeToParticipantTurn(
	block *ParticipantTurnBlock,
	event TranscriptEvent,
	policy participantTurnTranscriptPolicy,
) participantTurnTranscriptResult {
	if event.NarrativeKind == TranscriptNarrativeUser {
		return participantTurnTranscriptResult{}
	}
	if event.NarrativeKind == TranscriptNarrativeNotice || event.NarrativeKind == TranscriptNarrativeSystem {
		block.AddNotice(formatTranscriptNoticeText(event.Text), event.OccurredAt, event.NoticeKind)
		return participantTurnTranscriptResult{changed: true}
	}
	text := tuikit.SanitizeLogText(transcriptNarrativeText(event))
	if text == "" && !event.Final {
		return participantTurnTranscriptResult{}
	}
	source := narrativeSourceIdentityFromTranscriptEvent(event)
	switch event.NarrativeKind {
	case TranscriptNarrativeReasoning:
		if policy.appendFinalNarrative || !event.Final {
			if text != "" {
				block.AppendStreamEvent(SEReasoning, text, source, event.OccurredAt)
			}
		} else {
			block.ReplaceFinalStreamEvent(SEReasoning, text, source, event.OccurredAt)
		}
	default:
		if event.Final && !policy.appendFinalNarrative {
			closeLatestReasoningTiming(block.Events, event.OccurredAt)
			block.ReplaceFinalStreamEvent(SEAssistant, text, source, event.OccurredAt)
		} else if text != "" {
			block.AppendStreamEvent(SEAssistant, text, source, event.OccurredAt)
		}
	}
	if event.Final && event.NarrativeKind == TranscriptNarrativeAssistant && policy.completeOnFinalAssistant {
		block.SetStatus("completed", "", "", event.OccurredAt)
	} else if event.Final && event.NarrativeKind == TranscriptNarrativeReasoning &&
		strings.EqualFold(strings.TrimSpace(block.Status), "waiting_approval") {
		block.Status = "running"
	} else if state := strings.ToLower(strings.TrimSpace(block.Status)); state == "initializing" || state == "prompting" {
		block.Status = "running"
	}
	return participantTurnTranscriptResult{
		changed:  true,
		terminal: eventstream.IsTerminalLifecycleState(block.Status),
	}
}

func transcriptNarrativeText(event TranscriptEvent) string {
	if event.Final && event.NarrativeKind == TranscriptNarrativeAssistant && len(event.Citations) > 0 {
		return transcript.RenderCitationMarkdown(event.Text, event.Citations)
	}
	return event.Text
}

func setParticipantTurnTranscriptStatus(
	block *ParticipantTurnBlock,
	state string,
	approvalTool string,
	approvalCommand string,
	event TranscriptEvent,
	monotonic bool,
) {
	state = strings.ToLower(strings.TrimSpace(state))
	if block == nil || state == "" {
		return
	}
	if monotonic &&
		eventstream.IsTerminalLifecycleState(block.Status) &&
		!eventstream.IsTerminalLifecycleState(state) {
		return
	}
	block.SetStatus(state, approvalTool, approvalCommand, event.OccurredAt)
}
