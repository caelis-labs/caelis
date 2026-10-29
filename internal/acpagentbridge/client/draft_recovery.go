package client

import (
	"bytes"
	"encoding/json"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

// Recovery and validation belong to the SDK. Only copy fields owned by the
// recognized variant; unrelated extensions keep their original payload.
func recoverDraftUpdateFields(update v2.SessionUpdate, fields map[string]json.RawMessage) (bool, error) {
	var keys []string
	switch {
	case update.ToolCallUpdate != nil:
		keys = []string{"_meta", "content", "kind", "locations", "name", "status", "title"}
	case update.UserMessage != nil, update.AgentMessage != nil, update.AgentThought != nil:
		keys = []string{"_meta", "content"}
	case update.UserMessageChunk != nil, update.AgentMessageChunk != nil, update.AgentThoughtChunk != nil:
		keys = []string{"_meta", "content"}
	case update.TerminalUpdate != nil:
		keys = []string{"_meta", "command", "cwd", "exitStatus", "output", "size"}
	case update.ConfigOptionUpdate != nil:
		keys = []string{"_meta", "configOptions"}
	case update.AvailableCommandsUpdate != nil:
		keys = []string{"_meta", "availableCommands"}
	case update.PlanUpdate != nil:
		keys = []string{"_meta", "plan"}
	default:
		return false, nil
	}
	// Avoid materializing the SDK value for identity-only observations and
	// already empty content lists. These frequent frames cannot carry recovery.
	supplied := false
	for _, key := range keys {
		if raw, present := fields[key]; present {
			if key != "content" || !bytes.Equal(bytes.TrimSpace(raw), []byte("[]")) {
				supplied = true
				break
			}
		}
	}
	if !supplied {
		return false, nil
	}
	raw, err := json.Marshal(update)
	if err != nil {
		return false, err
	}
	var recovered map[string]json.RawMessage
	if err := json.Unmarshal(raw, &recovered); err != nil {
		return false, err
	}
	changed := false
	for _, key := range keys {
		if _, supplied := fields[key]; !supplied {
			continue
		}
		if value, present := recovered[key]; present {
			if !bytes.Equal(fields[key], value) {
				fields[key] = value
				changed = true
			}
		} else {
			delete(fields, key)
			changed = true
		}
	}
	return changed, nil
}

func recoverDraftReplacementLists(fields map[string]json.RawMessage) error {
	var kind string
	_ = json.Unmarshal(fields["sessionUpdate"], &kind)
	target := fields
	var receiver any
	var key string
	switch kind {
	case "available_commands_update":
		receiver, key = &v2.SessionAvailableCommandsUpdate{}, "availableCommands"
	case "config_option_update":
		receiver, key = &v2.SessionConfigOptionUpdate{}, "configOptions"
	case "plan_update":
		target = nil
		if err := json.Unmarshal(fields["plan"], &target); err != nil {
			return err
		}
		var planType string
		_ = json.Unmarshal(target["type"], &planType)
		if planType != "items" {
			return nil
		}
		receiver, key = &v2.PlanItems{}, "entries"
	default:
		return nil
	}
	raw, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, receiver); err != nil {
		return err
	}
	encoded, err := json.Marshal(receiver)
	if err != nil {
		return err
	}
	var recovered map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &recovered); err != nil {
		return err
	}
	target[key] = recovered[key]
	if kind == "plan_update" {
		fields["plan"], err = json.Marshal(target)
	}
	return err
}
