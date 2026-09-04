package taskstream

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

func terminalSubagentFallbackSnapshot(entry *task.Entry) fallbackSnapshot {
	if entry == nil {
		return fallbackSnapshot{}
	}
	turnID := firstString(mapString(entry.Metadata, "turn_id"), mapString(entry.Spec, "turn_id"), entry.Terminal.TerminalID)
	activityID := firstString(mapString(entry.Metadata, "child_activity_id"), mapString(entry.Spec, "child_activity_id"))
	sessionID := strings.TrimSpace(entry.Session.SessionID)
	text := display.SubagentTaskFinalText(string(entry.State), entry.Result)
	if strings.TrimSpace(text) == "" {
		text = strings.TrimSpace(entry.FailureDiagnostic)
	}
	frames := make([]Frame, 0, 1)
	if strings.TrimSpace(text) != "" {
		messageActivityID := firstString(activityID, turnID, entry.TaskID)
		messageID := strings.Join([]string{"subagent-terminal", strings.TrimSpace(entry.TaskID), messageActivityID}, ":")
		event := session.MarkUIOnly(&session.Event{
			ID: messageID, MessageID: messageID, SessionID: sessionID,
			Type: session.EventTypeAssistant, Time: entry.UpdatedAt, Text: text,
			Actor: session.ActorRef{
				Kind: session.ActorKindParticipant,
				ID:   firstString(mapString(entry.Metadata, "agent_id"), mapString(entry.Spec, "agent_id")),
				Role: string(session.ParticipantRoleDelegated),
				Name: firstString(mapString(entry.Metadata, "agent"), mapString(entry.Spec, "agent"), entry.Handle),
			},
			Scope: &session.EventScope{
				TurnID: turnID,
				Participant: session.ParticipantRef{
					ID:   firstString(mapString(entry.Metadata, "agent_id"), mapString(entry.Spec, "agent_id")),
					Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
					DelegationID: strings.TrimSpace(entry.TaskID),
				},
			},
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodSessionUpdate,
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
					MessageID:     messageID, Content: session.ProtocolTextContent(text),
				},
			},
		})
		frames = append(frames, Frame{
			TerminalID: turnID, ActivityID: activityID, Event: event, UpdatedAt: entry.UpdatedAt,
		})
	}
	return fallbackSnapshot{
		ActivityID: activityID, State: string(entry.State), Running: false,
		UpdatedAt: entry.UpdatedAt, Frames: frames,
	}
}

func (s *service) loadDurableSubagentHistory(
	ctx context.Context,
	entry *task.Entry,
) (fallbackSnapshot, error) {
	if s == nil || s.sessions == nil || s.subagentHistory == nil || entry == nil {
		return fallbackSnapshot{}, errorcode.New(errorcode.Unavailable, "taskstream: subagent ACP history is unavailable")
	}
	childSessionID := taskHistoryChildSessionID(entry)
	if childSessionID == "" {
		return fallbackSnapshot{}, errorcode.New(errorcode.FailedPrecondition, "taskstream: subagent child Session identity is unavailable")
	}
	loaded, err := s.loadProviderSubagentHistory(ctx, entry, childSessionID)
	if err != nil {
		return fallbackSnapshot{}, err
	}
	return durableSubagentHistorySnapshot(entry, loaded.Events), nil
}

func taskHistoryChildSessionID(entry *task.Entry) string {
	if entry == nil {
		return ""
	}
	return firstString(mapString(entry.Metadata, "session_id"), mapString(entry.Spec, "session_id"))
}

func (s *service) loadProviderSubagentHistory(
	ctx context.Context,
	entry *task.Entry,
	childSessionID string,
) (session.LoadedSession, error) {
	if s == nil || s.sessions == nil || s.subagentHistory == nil || entry == nil {
		return session.LoadedSession{}, errorcode.New(errorcode.Unavailable, "taskstream: provider-owned subagent history is unavailable")
	}
	target := taskHistoryTarget(entry)
	if err := delegation.ValidateTarget(target); err != nil {
		return session.LoadedSession{}, err
	}
	parent, err := s.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: entry.Session, Limit: 1})
	if err != nil {
		return session.LoadedSession{}, err
	}
	role := session.ParticipantRole(firstString(mapString(entry.Spec, "participant_role"), mapString(entry.Metadata, "participant_role")))
	if role == "" {
		role = session.ParticipantRoleDelegated
	}
	req := tasksubagent.HistoryRequest{
		Anchor: delegation.Anchor{
			TaskID: strings.TrimSpace(entry.TaskID), SessionID: strings.TrimSpace(childSessionID),
			AgentID: firstString(mapString(entry.Spec, "agent_id"), mapString(entry.Metadata, "agent_id"), entry.TaskID),
		},
		Reconnect: tasksubagent.ReconnectRequest{
			Spawn: tasksubagent.SpawnContext{
				SessionRef: session.NormalizeSessionRef(entry.Session), Session: session.CloneSession(parent.Session),
				CWD: strings.TrimSpace(parent.Session.CWD), TaskID: strings.TrimSpace(entry.TaskID),
				Handle: firstString(entry.Handle, mapString(entry.Spec, "handle"), mapString(entry.Metadata, "handle")),
				Role:   role, ParentCallID: firstString(mapString(entry.Metadata, "parent_call"), mapString(entry.Spec, "parent_call")),
				Mode: mapString(entry.Spec, "mode"), ApprovalMode: mapString(entry.Spec, "approval_mode"),
			},
			Target: target,
		},
	}
	return s.subagentHistory.LoadHistory(ctx, req)
}

func taskHistoryTarget(entry *task.Entry) delegation.Target {
	if entry == nil {
		return delegation.Target{}
	}
	encoded, err := json.Marshal(entry.Spec["target"])
	if err == nil {
		var target delegation.Target
		if json.Unmarshal(encoded, &target) == nil && target.Placement.Kind != "" {
			return delegation.NormalizeTarget(target)
		}
	}
	// Provider Session identity is meaningful only together with the exact
	// placement frozen by the original Spawn. Never resolve a legacy agent name
	// through current configuration and risk loading the Session from another
	// provider endpoint.
	return delegation.Target{}
}

func durableSubagentHistorySnapshot(entry *task.Entry, events []*session.Event) fallbackSnapshot {
	history := durableSubagentHistoryEvents(entry, events)
	activityID := firstString(mapString(entry.Metadata, "child_activity_id"), mapString(entry.Spec, "child_activity_id"))
	frames := make([]Frame, 0, len(history))
	for _, event := range history {
		turnID := ""
		if event.Scope != nil {
			turnID = strings.TrimSpace(event.Scope.TurnID)
		}
		frames = append(frames, Frame{
			TerminalID: turnID, ActivityID: activityID, Event: event, UpdatedAt: event.Time,
		})
	}
	return fallbackSnapshot{
		ActivityID: activityID, State: string(entry.State), Running: false,
		UpdatedAt: entry.UpdatedAt, Frames: frames,
		FinalText: "", TerminalFramed: false,
	}
}

func durableSubagentHistoryEvents(entry *task.Entry, events []*session.Event) []*session.Event {
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		cloned := session.CloneEvent(event)
		if cloned.Scope == nil {
			cloned.Scope = &session.EventScope{}
		}
		if cloned.Scope.Participant.Kind == "" {
			cloned.Scope.Participant.Kind = session.ParticipantKindSubagent
		}
		if cloned.Scope.Participant.ID == "" {
			cloned.Scope.Participant.ID = firstString(mapString(entry.Metadata, "agent_id"), mapString(entry.Spec, "agent_id"))
		}
		if cloned.Scope.Participant.Role == "" {
			cloned.Scope.Participant.Role = session.ParticipantRoleDelegated
		}
		if cloned.Scope.Participant.DelegationID == "" {
			cloned.Scope.Participant.DelegationID = strings.TrimSpace(entry.TaskID)
		}
		out = append(out, cloned)
	}
	return out
}
