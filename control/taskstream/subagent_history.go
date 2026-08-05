package taskstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

// readTaskSnapshot keeps live output on the owning Runtime. A terminal
// subagent whose Runtime no longer exists after process restart falls back to
// the durable child Session, which is the same canonical source used by ACP
// session/load. This observation never activates an execution Runtime.
func (s *service) readTaskSnapshot(
	ctx context.Context,
	entry *task.Entry,
	cursor stream.Cursor,
) (stream.Snapshot, bool, error) {
	var runtimeErr error
	streams := s.streams()
	if streams == nil {
		runtimeErr = errorcode.New(errorcode.Unavailable, "taskstream: runtime streams are unavailable")
	} else {
		snapshot, err := streams.Read(ctx, stream.ReadRequest{
			Ref:    stream.Ref{SessionID: entry.Session.SessionID, TaskID: entry.TaskID},
			Cursor: cursor,
		})
		if err == nil {
			if durableSubagentHistoryPreferred(entry, snapshot, cursor) {
				historical, historyErr := s.loadDurableSubagentHistory(ctx, entry, cursor)
				if historyErr == nil {
					return historical, true, nil
				}
				// The Runtime current-state snapshot is still usable when the
				// independently durable child Session cannot be loaded.
			}
			return snapshot, false, nil
		}
		runtimeErr = err
	}
	if !durableSubagentHistoryEligible(entry, runtimeErr) {
		return stream.Snapshot{}, false, runtimeErr
	}
	snapshot, err := s.loadDurableSubagentHistory(ctx, entry, cursor)
	if err != nil {
		return stream.Snapshot{}, false, errorcode.Wrap(
			errorcode.Unavailable,
			fmt.Sprintf("taskstream: load durable subagent history for %q", strings.TrimSpace(entry.TaskID)),
			errors.Join(runtimeErr, err),
		)
	}
	return snapshot, true, nil
}

func durableSubagentHistoryEligible(entry *task.Entry, runtimeErr error) bool {
	return terminalSubagentEntry(entry) &&
		errorcode.CodeOf(runtimeErr) == errorcode.Unavailable
}

// A Runtime reconstructed from the durable Task index can answer Read while
// retaining only the current terminal state. When its truncation boundary is
// ahead of the requested cursor, the child Session is the only complete source
// for the missing assistant history.
func durableSubagentHistoryPreferred(entry *task.Entry, snapshot stream.Snapshot, cursor stream.Cursor) bool {
	return terminalSubagentEntry(entry) && !snapshot.Running &&
		stream.IsTerminalState(snapshot.State) &&
		snapshot.EventsTruncatedBefore > cursor.Events
}

func terminalSubagentEntry(entry *task.Entry) bool {
	return entry != nil && entry.Kind == task.KindSubagent && !entry.Running &&
		stream.IsTerminalState(string(entry.State))
}

func (s *service) loadDurableSubagentHistory(
	ctx context.Context,
	entry *task.Entry,
	cursor stream.Cursor,
) (stream.Snapshot, error) {
	if s == nil || s.sessions == nil || entry == nil {
		return stream.Snapshot{}, errorcode.New(errorcode.Unavailable, "taskstream: durable Session history is unavailable")
	}
	childSessionID := firstString(mapString(entry.Metadata, "session_id"), mapString(entry.Spec, "session_id"))
	if childSessionID == "" {
		return stream.Snapshot{}, errorcode.New(errorcode.FailedPrecondition, "taskstream: subagent child Session identity is unavailable")
	}
	childRef := entry.Session
	childRef.SessionID = childSessionID
	loaded, err := s.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: childRef})
	if err != nil {
		return stream.Snapshot{}, err
	}
	return durableSubagentHistorySnapshot(entry, loaded.Events, cursor), nil
}

func durableSubagentHistorySnapshot(entry *task.Entry, events []*session.Event, cursor stream.Cursor) stream.Snapshot {
	ref := stream.Ref{
		SessionID: strings.TrimSpace(entry.Session.SessionID),
		TaskID:    strings.TrimSpace(entry.TaskID),
	}
	assistant := durableSubagentAssistantEvents(entry, events)
	frontier := max(taskStreamEventFrontier(entry), int64(len(assistant)))
	base := frontier - int64(len(assistant))
	frames := make([]stream.Frame, 0, len(assistant))
	for index, event := range assistant {
		eventCursor := base + int64(index) + 1
		if eventCursor <= cursor.Events {
			continue
		}
		turnID := ""
		if event.Scope != nil {
			turnID = strings.TrimSpace(event.Scope.TurnID)
		}
		frames = append(frames, stream.Frame{
			Ref:                   stream.Ref{SessionID: ref.SessionID, TaskID: ref.TaskID, TerminalID: turnID},
			Cursor:                stream.Cursor{Events: eventCursor},
			EventsTruncatedBefore: base,
			Event:                 event,
			UpdatedAt:             event.Time,
		})
	}
	currentTurnID := firstString(mapString(entry.Metadata, "turn_id"), mapString(entry.Spec, "turn_id"), entry.Terminal.TerminalID)
	ref.TerminalID = currentTurnID
	return stream.Snapshot{
		Ref:                   ref,
		Cursor:                stream.Cursor{Events: frontier},
		EventsTruncatedBefore: base,
		State:                 string(entry.State),
		Running:               false,
		TerminalFramed:        false,
		UpdatedAt:             entry.UpdatedAt,
		Frames:                frames,
	}
}

func durableSubagentAssistantEvents(entry *task.Entry, events []*session.Event) []*session.Event {
	turns := make(map[string]string)
	nextTurn := 0
	out := make([]*session.Event, 0)
	for _, event := range events {
		text := strings.TrimSpace(session.EventText(event))
		if event == nil || session.EventTypeOf(event) != session.EventTypeAssistant || text == "" {
			continue
		}
		updateType := strings.TrimSpace(session.ProtocolSessionUpdateType(event))
		if updateType != "" && updateType != string(session.ProtocolUpdateTypeAgentMessage) {
			continue
		}
		turnKey := durableSubagentTurnKey(event)
		turnID := turns[turnKey]
		if turnID == "" {
			nextTurn++
			turnID = fmt.Sprintf("%s:%d", strings.TrimSpace(entry.TaskID), nextTurn)
			turns[turnKey] = turnID
		}
		message := model.NewTextMessage(model.RoleAssistant, session.EventText(event))
		messageID := firstString(
			session.EventMessageID(event),
			strings.TrimSpace(event.ID),
			fmt.Sprintf("subagent-history:%s:%d:%d", strings.TrimSpace(entry.TaskID), nextTurn, len(out)+1),
		)
		out = append(out, &session.Event{
			ID:         strings.TrimSpace(event.ID),
			MessageID:  messageID,
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityUIOnly,
			Time:       event.Time,
			Actor:      event.Actor,
			Scope: &session.EventScope{
				TurnID: turnID,
				Source: "subagent_session_history",
				Participant: session.ParticipantRef{
					ID:           firstString(mapString(entry.Metadata, "agent_id"), mapString(entry.Spec, "agent_id")),
					Kind:         session.ParticipantKindSubagent,
					Role:         session.ParticipantRoleDelegated,
					DelegationID: strings.TrimSpace(entry.TaskID),
				},
			},
			Message: &message,
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodSessionUpdate,
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
					MessageID:     messageID,
					Content:       session.ProtocolTextContent(session.EventText(event)),
				},
			},
		})
	}
	return out
}

func durableSubagentTurnKey(event *session.Event) string {
	if event != nil && event.Scope != nil {
		if turnID := strings.TrimSpace(event.Scope.TurnID); turnID != "" {
			return "turn:" + turnID
		}
	}
	if messageID := session.EventMessageID(event); messageID != "" {
		return "message:" + messageID
	}
	if event != nil && strings.TrimSpace(event.ID) != "" {
		return "event:" + strings.TrimSpace(event.ID)
	}
	return "turn"
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
