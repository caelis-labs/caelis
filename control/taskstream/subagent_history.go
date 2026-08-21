package taskstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

// readTaskSnapshot keeps live observation on the owning Runtime. Finite reads
// may prefer a terminal child's read-only ACP session/load history. If an
// endpoint does not implement that optional capability and its execution
// Runtime has already been released, Control exposes only the bounded durable
// terminal result; it does not claim that fallback is complete history.
func (s *service) readTaskSnapshot(
	ctx context.Context,
	entry *task.Entry,
	cursor stream.Cursor,
	preferHistory bool,
) (stream.Snapshot, bool, error) {
	terminalSubagent := entry != nil && entry.Kind == task.KindSubagent && !entry.Running &&
		stream.IsTerminalState(string(entry.State))
	childSessionID := ""
	if terminalSubagent {
		childSessionID = taskHistoryChildSessionID(entry)
	}
	historyCandidate := childSessionID != ""
	if preferHistory && historyCandidate {
		// Once Spawn has created a provider child Session, its frozen endpoint is
		// part of the durable routing authority. A missing or malformed target must
		// fail closed; falling back here could silently read the current binding or
		// disguise a corrupt Task as a pre-Session failure.
		if err := delegation.ValidateTarget(taskHistoryTarget(entry)); err != nil {
			return stream.Snapshot{}, false, errorcode.Wrap(
				errorcode.FailedPrecondition,
				fmt.Sprintf("taskstream: frozen subagent history target for %q is unavailable", strings.TrimSpace(entry.TaskID)),
				err,
			)
		}
	}
	// A failure before the provider created a child Session, or Host composition
	// without an ACP history reader, cannot provide complete history. It may still
	// expose the bounded terminal Task result when no Runtime current state
	// remains, but must never read a child Session store directly.
	terminalFallbackEligible := preferHistory && terminalSubagent &&
		(!historyCandidate || s == nil || s.sessions == nil || s.subagentHistory == nil)
	if preferHistory && s != nil && s.sessions != nil && s.subagentHistory != nil && historyCandidate {
		snapshot, err := s.loadDurableSubagentHistory(ctx, entry, cursor)
		if err == nil {
			return snapshot, true, nil
		}
		if !errorcode.Is(err, errorcode.Unsupported) {
			return stream.Snapshot{}, false, errorcode.Wrap(
				errorcode.Unavailable,
				fmt.Sprintf("taskstream: load durable subagent history for %q", strings.TrimSpace(entry.TaskID)),
				err,
			)
		}
		terminalFallbackEligible = true
		// ACP session/load is optional for external Agents. When the exact child
		// endpoint cannot provide it, retain the Runtime current-state path rather
		// than making the terminal Task unobservable. This fallback never reads a
		// child Session file and never pretends its retained prefix is complete.
	}
	streams := s.streams()
	if streams == nil {
		if terminalFallbackEligible {
			return terminalSubagentFallbackSnapshot(entry, cursor), true, nil
		}
		return stream.Snapshot{}, false, errorcode.New(errorcode.Unavailable, "taskstream: runtime streams are unavailable")
	}
	snapshot, err := streams.Read(ctx, stream.ReadRequest{
		Ref:    stream.Ref{SessionID: entry.Session.SessionID, TaskID: entry.TaskID},
		Cursor: cursor,
	})
	if err != nil && terminalFallbackEligible && errorcode.Is(err, errorcode.Unavailable) {
		return terminalSubagentFallbackSnapshot(entry, cursor), true, nil
	}
	return snapshot, false, err
}

func terminalSubagentFallbackSnapshot(entry *task.Entry, cursor stream.Cursor) stream.Snapshot {
	ref := stream.Ref{}
	if entry == nil {
		return stream.Snapshot{Ref: ref, Cursor: stream.CloneCursor(cursor)}
	}
	turnID := firstString(mapString(entry.Metadata, "turn_id"), mapString(entry.Spec, "turn_id"), entry.Terminal.TerminalID)
	activityID := firstString(mapString(entry.Metadata, "child_activity_id"), mapString(entry.Spec, "child_activity_id"))
	ref = stream.Ref{
		SessionID: strings.TrimSpace(entry.Session.SessionID),
		TaskID:    strings.TrimSpace(entry.TaskID), TerminalID: turnID,
	}
	frontier := max(taskStreamEventFrontier(entry), int64(1))
	boundary := max(frontier, cursor.Events)
	replayCurrentState := cursor.Events < frontier
	text := display.SubagentTaskFinalText(string(entry.State), entry.Result)
	if strings.TrimSpace(text) == "" {
		text = strings.TrimSpace(entry.FailureDiagnostic)
	}
	frames := make([]stream.Frame, 0, 1)
	if replayCurrentState && strings.TrimSpace(text) != "" {
		messageActivityID := firstString(activityID, turnID, entry.TaskID)
		messageID := strings.Join([]string{"subagent-terminal", strings.TrimSpace(entry.TaskID), messageActivityID}, ":")
		event := session.MarkUIOnly(&session.Event{
			ID: messageID, MessageID: messageID, SessionID: ref.SessionID,
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
		frames = append(frames, stream.Frame{
			Ref: ref, ActivityID: activityID, Cursor: stream.Cursor{Events: boundary},
			EventsTruncatedBefore: frontier, Event: event, UpdatedAt: entry.UpdatedAt,
		})
	}
	return stream.Snapshot{
		Ref: ref, ActivityID: activityID, Cursor: stream.Cursor{Events: boundary},
		EventsTruncatedBefore: frontier, State: string(entry.State), Running: false,
		TerminalFramed: !replayCurrentState, UpdatedAt: entry.UpdatedAt, Frames: frames,
	}
}

func (s *service) loadDurableSubagentHistory(
	ctx context.Context,
	entry *task.Entry,
	cursor stream.Cursor,
) (stream.Snapshot, error) {
	if s == nil || s.sessions == nil || s.subagentHistory == nil || entry == nil {
		return stream.Snapshot{}, errorcode.New(errorcode.Unavailable, "taskstream: subagent ACP history is unavailable")
	}
	childSessionID := taskHistoryChildSessionID(entry)
	if childSessionID == "" {
		return stream.Snapshot{}, errorcode.New(errorcode.FailedPrecondition, "taskstream: subagent child Session identity is unavailable")
	}
	loaded, err := s.loadProviderSubagentHistory(ctx, entry, childSessionID)
	if err != nil {
		return stream.Snapshot{}, err
	}
	return durableSubagentHistorySnapshot(entry, loaded.Events, cursor), nil
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

func durableSubagentHistorySnapshot(entry *task.Entry, events []*session.Event, cursor stream.Cursor) stream.Snapshot {
	ref := stream.Ref{
		SessionID: strings.TrimSpace(entry.Session.SessionID),
		TaskID:    strings.TrimSpace(entry.TaskID),
	}
	history := durableSubagentHistoryEvents(entry, events)
	activityID := firstString(mapString(entry.Metadata, "child_activity_id"), mapString(entry.Spec, "child_activity_id"))
	frontier := max(taskStreamEventFrontier(entry), int64(len(history)))
	boundary := max(frontier, cursor.Events)
	replayCurrentState := cursor.Events < frontier
	frames := make([]stream.Frame, 0, len(history))
	if replayCurrentState {
		for _, event := range history {
			turnID := ""
			if event.Scope != nil {
				turnID = strings.TrimSpace(event.Scope.TurnID)
			}
			frames = append(frames, stream.Frame{
				Ref:                   stream.Ref{SessionID: ref.SessionID, TaskID: ref.TaskID, TerminalID: turnID},
				ActivityID:            activityID,
				Cursor:                stream.Cursor{Events: boundary},
				EventsTruncatedBefore: frontier,
				Event:                 event,
				UpdatedAt:             event.Time,
			})
		}
	}
	currentTurnID := firstString(mapString(entry.Metadata, "turn_id"), mapString(entry.Spec, "turn_id"), entry.Terminal.TerminalID)
	ref.TerminalID = currentTurnID
	return stream.Snapshot{
		Ref:                   ref,
		ActivityID:            activityID,
		Cursor:                stream.Cursor{Events: boundary},
		EventsTruncatedBefore: frontier,
		State:                 string(entry.State),
		Running:               false,
		TerminalFramed:        !replayCurrentState,
		UpdatedAt:             entry.UpdatedAt,
		Frames:                frames,
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

func taskStreamEventFrontier(entry *task.Entry) int64 {
	if entry == nil {
		return 0
	}
	var frontier int64
	for _, value := range []any{entry.Metadata["stream_event_cursor"], entry.EventCursor} {
		switch typed := value.(type) {
		case int:
			frontier = max(frontier, int64(typed))
		case int64:
			frontier = max(frontier, typed)
		case uint64:
			if typed <= ^uint64(0)>>1 {
				frontier = max(frontier, int64(typed))
			}
		case float64:
			if typed >= 0 {
				frontier = max(frontier, int64(typed))
			}
		case json.Number:
			if parsed, err := typed.Int64(); err == nil && parsed >= 0 {
				frontier = max(frontier, parsed)
			}
		}
	}
	return frontier
}
