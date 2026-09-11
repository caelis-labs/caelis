package taskstream

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

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
	if s.recorder == nil {
		return session.LoadedSession{}, errorcode.New(errorcode.Unavailable, "Task output recorder is unavailable")
	}
	descriptor := descriptorFromEntry(entry)
	req.Reconnect.Spawn.ActivityID = descriptor.ActivityID
	req.Reconnect.Spawn.Output = s.recorder.BindTaskOutput(ctx, output.Binding{SessionID: entry.Session.SessionID, TaskID: entry.TaskID, ActivityID: descriptor.ActivityID, Kind: output.TaskKindSubagent})
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
