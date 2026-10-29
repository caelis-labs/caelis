package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

// draftBridge adapts the existing normalized projection owner to alpha.8.
// Version-specific lifecycle and insertion facts come from native notifications,
// never from the stable blocking Prompt response.
type draftBridge struct{ agent *draftAgent }

func (b *draftBridge) Close() error { return b.agent.connection.Close() }
func (b *draftBridge) SessionUpdate(ctx context.Context, notification acp.SessionNotification) error {
	updates, err := draftUpdates(string(notification.SessionId), notification.Update)
	if err != nil {
		return err
	}
	for _, update := range updates {
		if err = b.agent.connection.SessionUpdate(ctx, v2.UpdateSessionNotification{SessionId: v2.SessionId(notification.SessionId), Update: update}); err != nil {
			return err
		}
	}
	return nil
}
func (b *draftBridge) RequestPermission(ctx context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	d := b.agent
	state, err := d.core.lookupSession(string(request.SessionId))
	if err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	state.mu.Lock()
	turnID := state.activeTurnID
	state.mu.Unlock()
	session := d.session(state.threadID)
	permissionCtx, cancel := context.WithCancel(ctx)
	session.mu.Lock()
	if turnID == "" || session.cancelRequested || session.terminalTurn == turnID {
		session.mu.Unlock()
		cancel()
		return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
	}
	if session.permissionCancels == nil {
		session.permissionCancels = map[uint64]draftPermission{}
	}
	session.nextPermission++
	token := session.nextPermission
	session.permissionCancels[token] = draftPermission{activity: turnID, cancel: cancel}
	session.mu.Unlock()
	defer func() {
		cancel()
		session.mu.Lock()
		delete(session.permissionCancels, token)
		session.mu.Unlock()
		if current, lookupErr := d.core.lookupSession(state.threadID); lookupErr == nil && current == state && state.route.failure() == nil {
			_ = d.sendState(state.threadID, turnID, v2.RunningUpdate())
		}
	}()
	if err = d.sendState(state.threadID, turnID, v2.RequiresActionUpdate()); err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	options, err := draftConvert[[]v2.PermissionOption](request.Options)
	if err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	tool, err := draftConvert[v2.ToolCallUpdate](request.ToolCall)
	if err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	title := "Codex request"
	if request.ToolCall.Title != nil {
		title = *request.ToolCall.Title
	}
	response, err := d.connection.RequestPermission(permissionCtx, v2.RequestPermissionRequest{SessionId: v2.SessionId(request.SessionId), Options: options, Title: title,
		Subject: &v2.RequestPermissionSubject{ToolCall: &v2.RequestPermissionSubjectToolCall{Type: "tool_call", ToolCall: tool}},
	})
	if err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	return draftConvert[acp.RequestPermissionResponse](response)
}

func draftUpdates(sessionID string, update acp.SessionUpdate) ([]v2.SessionUpdate, error) {
	raw, err := json.Marshal(update)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var discriminator string
	_ = json.Unmarshal(fields["sessionUpdate"], &discriminator)
	var updates []v2.SessionUpdate
	switch discriminator {
	case "agent_message_chunk", "agent_thought_chunk", "user_message_chunk":
		var messageID string
		if json.Unmarshal(fields["messageId"], &messageID) != nil || messageID == "" {
			return nil, errors.New("codex adapter: draft message projection requires native item identity")
		}
	case "tool_call", "tool_call_update":
		// alpha.8 unifies creation and patch updates. Terminal metadata is replaced
		// by standard terminal_update/output_chunk before the associated tool patch.
		fields["sessionUpdate"] = json.RawMessage(`"tool_call_update"`)
		var meta map[string]json.RawMessage
		_ = json.Unmarshal(fields["_meta"], &meta)
		terminalUpdates, err := draftTerminalUpdates(meta)
		if err != nil {
			return nil, err
		}
		updates = append(updates, terminalUpdates...)
		for _, key := range []string{terminalInfoMetaKey, terminalOutputMetaKey, terminalOutputDeltaMetaKey, terminalExitMetaKey} {
			delete(meta, key)
		}
		if len(meta) == 0 {
			delete(fields, "_meta")
		} else {
			fields["_meta"], err = json.Marshal(meta)
			if err != nil {
				return nil, err
			}
		}
	case "plan":
		var entries []v2.PlanEntry
		if err = json.Unmarshal(fields["entries"], &entries); err != nil {
			return nil, err
		}
		return []v2.SessionUpdate{{PlanUpdate: &v2.SessionPlanUpdate{SessionUpdate: "plan_update", Plan: v2.PlanUpdateContent{Items: &v2.PlanUpdateContentItems{Type: "items", PlanId: v2.PlanId(stableID(sessionID, "plan", "plan")), Entries: entries}}}}}, nil
	case "config_option_update":
		options, err := draftConfigOptions(update.ConfigOptionUpdate.ConfigOptions)
		if err != nil {
			return nil, err
		}
		return []v2.SessionUpdate{{ConfigOptionUpdate: &v2.SessionConfigOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: options}}}, nil
	case "notice", "usage_update", "available_commands_update", "session_info_update":
	default:
		return nil, fmt.Errorf("codex adapter: unsupported draft projection %q", discriminator)
	}
	raw, err = json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	var result v2.SessionUpdate
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.Other != nil {
		return nil, fmt.Errorf("codex adapter: projection does not implement draft variant %q", discriminator)
	}
	return append(updates, result), nil
}
func draftTerminalUpdates(meta map[string]json.RawMessage) ([]v2.SessionUpdate, error) {
	var updates []v2.SessionUpdate
	var info struct {
		TerminalID string `json:"terminal_id"`
		CWD        string `json:"cwd"`
	}
	if raw, present := meta[terminalInfoMetaKey]; present {
		if err := json.Unmarshal(raw, &info); err != nil || info.TerminalID == "" {
			return nil, errors.New("codex adapter: invalid terminal info")
		}
		terminal := &v2.SessionTerminalUpdate{SessionUpdate: "terminal_update", TerminalId: v2.TerminalId(info.TerminalID)}
		if info.CWD != "" {
			cwd := v2.AbsolutePath(info.CWD)
			terminal.Cwd = &cwd
		}
		updates = append(updates, v2.SessionUpdate{TerminalUpdate: terminal})
	}
	for _, key := range []string{terminalOutputMetaKey, terminalOutputDeltaMetaKey} {
		if raw, present := meta[key]; present {
			var output struct {
				TerminalID string `json:"terminal_id"`
				Data       string `json:"data"`
			}
			if err := json.Unmarshal(raw, &output); err != nil || output.TerminalID == "" {
				return nil, errors.New("codex adapter: invalid terminal output")
			}
			updates = append(updates, v2.SessionUpdate{TerminalOutputChunk: &v2.SessionUpdateTerminalOutputChunk{SessionUpdate: "terminal_output_chunk", TerminalId: v2.TerminalId(output.TerminalID), Data: base64.StdEncoding.EncodeToString([]byte(output.Data))}})
		}
	}
	if raw, present := meta[terminalExitMetaKey]; present {
		var exit struct {
			TerminalID string  `json:"terminal_id"`
			ExitCode   *int64  `json:"exit_code"`
			Signal     *string `json:"signal"`
		}
		if err := json.Unmarshal(raw, &exit); err != nil || exit.TerminalID == "" {
			return nil, errors.New("codex adapter: invalid terminal exit")
		}
		// Native command execution uses signed exit codes, including -1 when
		// no process exit code is available. ACP represents that fact with an
		// exitStatus whose optional exitCode is absent; never wrap the sentinel.
		var code *int64
		if exit.ExitCode != nil && *exit.ExitCode >= 0 {
			code = acp.Ptr(*exit.ExitCode)
		}
		updates = append(updates, v2.SessionUpdate{TerminalUpdate: &v2.SessionTerminalUpdate{SessionUpdate: "terminal_update", TerminalId: v2.TerminalId(exit.TerminalID), ExitStatus: &v2.TerminalExitStatus{ExitCode: code, Signal: exit.Signal}}})
	}
	return updates, nil
}
