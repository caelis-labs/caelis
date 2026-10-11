package acpagentbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

func draftPromptContent(req v2.PromptRequest) (string, []model.ContentPart, error) {
	if err := req.Validate(); err != nil {
		return "", nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	blocks := make([]json.RawMessage, 0, len(req.Prompt))
	for _, block := range req.Prompt {
		if block.ResourceLink != nil {
			link := block.ResourceLink
			blocks = append(blocks, draftRaw(map[string]any{"type": "text", "text": link.Name + ": " + link.Uri}))
			continue
		}
		if block.Text == nil && block.Image == nil {
			return "", nil, acp.NewInvalidParams(map[string]any{"error": "unsupported prompt content"})
		}
		raw, err := json.Marshal(block)
		if err != nil {
			return "", nil, err
		}
		blocks = append(blocks, raw)
	}
	input, parts, err := promptContent(blocks)
	if err == nil && input == "" && len(parts) == 0 {
		err = acp.NewInvalidParams(map[string]any{"error": "prompt requires content"})
	}
	return input, parts, err
}

func draftEnvelopeUpdate(env eventstream.Envelope, replacement bool) (*v2.SessionUpdate, error) {
	env.Update = draftNormalizeTerminalContent(env.Update)
	raw, err := json.Marshal(env.Update)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	kind := env.Update.SessionUpdateType()
	switch kind {
	case eventstream.UpdateUserMessage, eventstream.UpdateAgentMessage, eventstream.UpdateAgentThought:
		var id string
		_ = json.Unmarshal(object["messageId"], &id)
		if kind == eventstream.UpdateUserMessage && env.EventID != "" && env.Final {
			id = env.EventID
		}
		if id == "" {
			id = env.EventID
		}
		if id == "" {
			return nil, fmt.Errorf("ACP draft %s has no canonical message identity", kind)
		}
		object["messageId"] = draftRaw(id)
		var replaces bool
		_ = json.Unmarshal(object["replacement"], &replaces)
		delete(object, "replacement")
		if env.Final || replacement || replaces {
			object["sessionUpdate"] = draftRaw(strings.TrimSuffix(kind, "_chunk"))
			if content, present := object["content"]; present {
				object["content"] = draftRaw([]json.RawMessage{content})
			}
		}
	case eventstream.UpdateToolCall, eventstream.UpdateToolCallInfo:
		object["sessionUpdate"] = draftRaw("tool_call_update")
		var clears []string
		_ = json.Unmarshal(object["replacedFields"], &clears)
		for _, field := range clears {
			if _, present := object[field]; !present {
				object[field] = draftRaw(nil)
			}
		}
		delete(object, "replacedFields")
		var meta map[string]json.RawMessage
		if json.Unmarshal(object["_meta"], &meta) == nil {
			delete(meta, acpmeta.TerminalInfoKey)
			delete(meta, acpmeta.TerminalOutputKey)
			delete(meta, acpmeta.TerminalOutputDeltaKey)
			delete(meta, acpmeta.TerminalExitKey)
			object["_meta"] = draftRaw(meta)
		}
	case eventstream.UpdatePlan:
		id := env.ScopeID
		if id == "" {
			id = env.SessionID
		}
		object["sessionUpdate"] = draftRaw("plan_update")
		entries := object["entries"]
		if len(entries) == 0 || string(entries) == "null" {
			entries = draftRaw([]any{})
		}
		object["plan"] = draftRaw(map[string]any{"type": "items", "planId": "native-plan:" + id, "entries": entries})
		delete(object, "entries")
	case eventstream.UpdateConfigOption:
		var options []acp.SessionConfigOption
		if err := json.Unmarshal(object["configOptions"], &options); err != nil {
			return nil, err
		}
		configs, err := draftConfigOptions(options)
		if err != nil {
			return nil, err
		}
		object["configOptions"] = draftRaw(configs)
	case "available_commands_update":
		var commands []acp.AvailableCommand
		if err := json.Unmarshal(object["availableCommands"], &commands); err != nil {
			return nil, err
		}
		object["availableCommands"] = draftRaw(draftCommands(commands))
	case eventstream.UpdateCurrentMode:
		// v2 has no parallel mode API. The shared configuration projection
		// carries the same native selection through config_option_update.
		return nil, nil
	}
	var update v2.SessionUpdate
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, &update); err != nil {
		return nil, err
	}
	if err := update.Validate(); err != nil {
		return nil, fmt.Errorf("ACP draft projected update: %w", err)
	}
	// alpha.8's typed marshaler loses most explicit null/empty patch fields.
	// Preserve the validated standard payload through its raw union branch.
	wire := json.RawMessage(encoded)
	return &v2.SessionUpdate{Other: &wire}, nil
}

func draftNormalizeTerminalContent(update eventstream.Update) eventstream.Update {
	switch value := update.(type) {
	case eventstream.ToolCall:
		value.Meta, value.Content = terminalExtensionMetaFromACPContent(value.Meta, value.ToolCallID, value.Content)
		return value
	case eventstream.ToolCallUpdate:
		value.Meta, value.Content = terminalExtensionMetaFromACPContent(value.Meta, value.ToolCallID, value.Content)
		return value
	case *eventstream.ToolCall:
		return draftNormalizeTerminalContent(*value)
	case *eventstream.ToolCallUpdate:
		return draftNormalizeTerminalContent(*value)
	}
	return update
}

func draftTerminalUpdates(env eventstream.Envelope) ([]v2.SessionUpdate, error) {
	update := draftNormalizeTerminalContent(env.Update)
	var meta map[string]any
	switch value := update.(type) {
	case eventstream.ToolCall:
		meta = value.Meta
	case eventstream.ToolCallUpdate:
		meta = value.Meta
	default:
		return nil, nil
	}
	info, hasInfo := acpmeta.ReadTerminalInfo(meta)
	output, hasOutput := acpmeta.ReadTerminalOutput(meta)
	exit, hasExit := acpmeta.ReadTerminalExit(meta)
	id := info.TerminalID
	if id == "" {
		id = output.TerminalID
	}
	if id == "" {
		id = exit.TerminalID
	}
	if !hasInfo && !hasOutput && !hasExit {
		return nil, nil
	}
	object := map[string]any{"sessionUpdate": "terminal_update", "terminalId": id}
	var updates []v2.SessionUpdate
	if hasOutput {
		data := base64.StdEncoding.EncodeToString([]byte(output.Data))
		if output.Replacement {
			object["output"] = map[string]any{"data": data}
		} else {
			var chunk v2.SessionUpdate
			if err := draftJSONConvert(map[string]any{"sessionUpdate": "terminal_output_chunk", "terminalId": id, "data": data}, &chunk); err != nil {
				return nil, err
			}
			updates = append(updates, chunk)
		}
	}
	if hasExit {
		status := map[string]any{}
		if exit.ExitCode != nil {
			if *exit.ExitCode >= 0 {
				status["exitCode"] = *exit.ExitCode

			}
		}
		if exit.Signal != nil {
			status["signal"] = *exit.Signal
		}
		object["exitStatus"] = status
	}
	var projected v2.SessionUpdate
	if err := draftJSONConvert(object, &projected); err != nil {
		return nil, err
	}
	return append(updates, projected), nil
}

func draftStateUpdate(active, action bool, activityID, stopReason string) (v2.SessionUpdate, error) {
	state := "idle"
	if active {
		state = "running"
	}
	if action {
		state = "requires_action"
	}
	object := map[string]any{"sessionUpdate": "state_update", "state": state, "_meta": acpdraft.ActivityMeta(activityID)}
	if state == "idle" {
		if stopReason == "" {
			stopReason = "none"
		}
		object["stopReason"] = stopReason
	}
	var result v2.SessionUpdate
	err := draftJSONConvert(object, &result)
	return result, err
}

func draftPermissionRequest(sessionID string, approval *session.ProtocolApproval) (v2.RequestPermissionRequest, error) {
	if approval == nil {
		return v2.RequestPermissionRequest{}, fmt.Errorf("ACP draft permission has no native payload")
	}
	wire, err := acppermission.EncodePermissionRequest(session.SessionRef{SessionID: sessionID}, approval, nil)
	if err != nil {
		return v2.RequestPermissionRequest{}, err
	}
	request := v2.RequestPermissionRequest{SessionId: v2.SessionId(sessionID), Title: firstNonEmptyNoticeMessageID(wire.Title, "Permission required"), Description: wire.Description, Meta: map[string]json.RawMessage{
		"caelis/approval_subject": draftRaw(session.ApprovalDisplaySubjectOf(approval)),
	}}
	if wire.ToolCall.ToolCallID != "" {
		var tool v2.ToolCallUpdate
		if err := draftJSONConvert(wire.ToolCall, &tool); err != nil {
			return request, err
		}
		request.Subject = &v2.RequestPermissionSubject{ToolCall: &v2.RequestPermissionSubjectToolCall{Type: "tool_call", ToolCall: tool}}
		if wire.Title == "" && wire.ToolCall.Title != nil {
			request.Title = firstNonEmptyNoticeMessageID(*wire.ToolCall.Title, "Permission required")
		}
	}
	if err := draftJSONConvert(wire.Options, &request.Options); err != nil {
		return request, err
	}
	return request, request.Validate()
}

func draftRaw(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

func draftConfigOptions(options []acp.SessionConfigOption) ([]v2.SessionConfigOption, error) {
	result := make([]v2.SessionConfigOption, 0, len(options))
	for _, option := range options {
		var fields map[string]json.RawMessage
		if err := draftJSONConvert(option, &fields); err != nil {
			return nil, err
		}
		fields["configId"] = fields["id"]
		delete(fields, "id")
		var mapped v2.SessionConfigOption
		if err := draftJSONConvert(fields, &mapped); err != nil {
			return nil, err
		}
		result = append(result, mapped)
	}
	return result, nil
}

func draftCommands(commands []acp.AvailableCommand) []v2.AvailableCommand {
	result := make([]v2.AvailableCommand, 0, len(commands))
	for _, command := range commands {
		mapped := v2.AvailableCommand{Name: command.Name, Description: command.Description, Meta: command.Meta}
		if command.Input != nil && command.Input.Unstructured != nil {
			mapped.Input = &v2.AvailableCommandInput{Text: &v2.AvailableCommandInputText{Type: "text", Hint: command.Input.Unstructured.Hint, Meta: command.Input.Unstructured.Meta}}
		}
		result = append(result, mapped)
	}
	return result
}

func (a *DraftAgent) draftAvailableCommands(ctx context.Context, sessionID string) ([]v2.AvailableCommand, error) {
	commands, err := a.inner.AvailableCommands(ctx, sessionID)
	return draftCommands(commands), err
}

// draftRefreshPresentation reads the shared native projection after the Router's
// semantic effects. It does not reimplement mode, model or command ownership.
func (a *DraftAgent) draftRefreshPresentation(ctx context.Context, sessionID string, commands bool) error {
	if a.inner.presentationClient == nil {
		return nil
	}
	snapshot, err := a.inner.presentationClient.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: sessionID})
	if err != nil {
		return err
	}
	_, configs, available := acpPresentationSnapshot(snapshot)
	options, err := draftConfigOptions(configs)
	if err != nil {
		return err
	}
	var update v2.SessionUpdate
	if err := draftJSONConvert(map[string]any{"sessionUpdate": "config_option_update", "configOptions": options}, &update); err != nil {
		return err
	}
	if err := a.callbacks.SessionUpdate(ctx, v2.UpdateSessionNotification{SessionId: v2.SessionId(sessionID), Update: update}); err != nil {
		return err
	}
	if commands {
		if err := draftJSONConvert(map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": draftCommands(available)}, &update); err != nil {
			return err
		}
		return a.callbacks.SessionUpdate(ctx, v2.UpdateSessionNotification{SessionId: v2.SessionId(sessionID), Update: update})
	}
	return nil
}
