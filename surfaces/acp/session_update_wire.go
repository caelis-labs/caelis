package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func sessionNotificationForWire(notification eventstream.SessionNotification) (any, error) {
	if notification.Update == nil {
		return nil, fmt.Errorf("acp surface: session update is required")
	}
	updateType := strings.TrimSpace(notification.Update.SessionUpdateType())
	if updateType == "" {
		return nil, fmt.Errorf("acp surface: session update type is required")
	}
	if !standardSessionUpdateType(updateType) {
		return notification, nil
	}
	raw, err := json.Marshal(notification)
	if err != nil {
		return nil, fmt.Errorf("acp surface: encode session notification: %w", err)
	}
	var wire acpsdk.SessionNotification
	if err := json.Unmarshal(raw, &wire); err != nil {
		// External Agents may emit future content-block variants inside an
		// otherwise standard update. Keep those display payloads readable until
		// ingress normalizes them to a standard ACP content block.
		if sessionUpdateAllowsExtensionContent(raw, updateType) {
			return notification, nil
		}
		return nil, fmt.Errorf("acp surface: normalize session notification: %w", err)
	}
	if err := wire.Validate(); err != nil {
		return nil, fmt.Errorf("acp surface: validate session notification: %w", err)
	}
	return wire, nil
}

func standardSessionUpdateType(updateType string) bool {
	switch updateType {
	case eventstream.UpdateUserMessage,
		eventstream.UpdateAgentMessage,
		eventstream.UpdateAgentThought,
		eventstream.UpdateToolCall,
		eventstream.UpdateToolCallInfo,
		eventstream.UpdatePlan,
		eventstream.UpdateAvailableCmds,
		eventstream.UpdateCurrentMode,
		eventstream.UpdateConfigOption,
		eventstream.UpdateSessionInfo,
		eventstream.UpdateUsage:
		return true
	default:
		return false
	}
}

func sessionUpdateAllowsExtensionContent(notification []byte, updateType string) bool {
	var notificationObject map[string]json.RawMessage
	if json.Unmarshal(notification, &notificationObject) != nil {
		return false
	}
	var updateObject map[string]json.RawMessage
	if json.Unmarshal(notificationObject["update"], &updateObject) != nil {
		return false
	}
	content := updateObject["content"]
	if len(content) == 0 {
		return false
	}
	hasExtension := false
	switch updateType {
	case eventstream.UpdateUserMessage, eventstream.UpdateAgentMessage, eventstream.UpdateAgentThought:
		if !contentBlockUsesExtension(content) {
			return false
		}
		hasExtension = true
		updateObject["content"] = standardTextContentBlock()
	case eventstream.UpdateToolCall, eventstream.UpdateToolCallInfo:
		var items []json.RawMessage
		if json.Unmarshal(content, &items) != nil {
			return false
		}
		for index, item := range items {
			var itemObject map[string]json.RawMessage
			if json.Unmarshal(item, &itemObject) != nil {
				return false
			}
			var itemType string
			_ = json.Unmarshal(itemObject["type"], &itemType)
			switch strings.TrimSpace(itemType) {
			case "content":
				if contentBlockUsesExtension(itemObject["content"]) {
					hasExtension = true
					itemObject["content"] = standardTextContentBlock()
					items[index] = marshalRawOrNil(itemObject)
				}
			case "diff", "terminal":
			default:
				if strings.TrimSpace(itemType) != "" {
					hasExtension = true
					items[index] = json.RawMessage(`{"type":"content","content":{"type":"text","text":""}}`)
				}
			}
		}
		if !hasExtension {
			return false
		}
		updateObject["content"] = marshalRawOrNil(items)
	default:
		return false
	}
	if !hasExtension {
		return false
	}
	notificationObject["update"] = marshalRawOrNil(updateObject)
	sanitized := marshalRawOrNil(notificationObject)
	var wire acpsdk.SessionNotification
	return json.Unmarshal(sanitized, &wire) == nil && wire.Validate() == nil
}

func contentBlockUsesExtension(raw json.RawMessage) bool {
	var block struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &block) != nil {
		return false
	}
	switch strings.TrimSpace(block.Type) {
	case "text", "image", "audio", "resource_link", "resource":
		return false
	default:
		return strings.TrimSpace(block.Type) != ""
	}
}

func standardTextContentBlock() json.RawMessage {
	return json.RawMessage(`{"type":"text","text":""}`)
}

func marshalRawOrNil(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}
