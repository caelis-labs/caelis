package session

import "strings"

func ensureProtocolText(event *Event) {
	if event == nil {
		return
	}
	text := runtimeText(event)
	if text == "" {
		return
	}
	updateType := ""
	switch EventTypeOf(event) {
	case EventTypeUser:
		updateType = string(ProtocolUpdateTypeUserMessage)
	case EventTypeAssistant:
		updateType = ProtocolSessionUpdateType(event)
		if updateType == "" {
			updateType = string(ProtocolUpdateTypeAgentMessage)
		}
	case EventTypeToolCall:
		updateType = string(ProtocolUpdateTypeToolCall)
	case EventTypeCompact:
		if event.Protocol == nil {
			event.Protocol = &EventProtocol{Method: ProtocolMethodContextCheckpoint}
		}
		updateType = "compact"
	default:
		return
	}
	if event.Protocol == nil {
		event.Protocol = &EventProtocol{}
	}
	protocol := CloneEventProtocol(*event.Protocol)
	if protocol.Update == nil {
		protocol.Update = &ProtocolUpdate{}
	}
	if protocol.Update.SessionUpdate == "" {
		protocol.Update.SessionUpdate = updateType
	}
	if protocol.Update.Content == nil {
		protocol.Update.Content = ProtocolTextContent(text)
	}
	event.Protocol = &protocol
}

func removeModelProjectionContent(event *Event) {
	if event == nil || event.Message == nil || event.Protocol == nil {
		return
	}
	protocol := CloneEventProtocol(*event.Protocol)
	if protocol.Update == nil {
		event.Protocol = &protocol
		return
	}
	if protocolContentIsText(protocol.Update.Content, event.Message.TextContent()) {
		protocol.Update.Content = nil
	}
	switch EventTypeOf(event) {
	case EventTypeUser, EventTypeAssistant:
		if protocolUpdateHasOnlySessionUpdate(protocol.Update) && protocol.Permission == nil {
			protocol.Update = nil
		}
	}
	if protocol.Method == ProtocolMethodSessionUpdate && protocol.Update == nil && protocol.Permission == nil {
		event.Protocol = nil
		return
	}
	event.Protocol = &protocol
}

func removeToolProjectionProtocol(event *Event) {
	if event == nil || event.Tool == nil || event.Protocol == nil {
		return
	}
	protocol := CloneEventProtocol(*event.Protocol)
	if protocol.Update != nil {
		switch strings.TrimSpace(protocol.Update.SessionUpdate) {
		case string(ProtocolUpdateTypeToolCall), string(ProtocolUpdateTypeToolUpdate):
			protocol.Update = nil
			protocol.ToolCall = nil
			protocol.UpdateType = ""
		}
	}
	if protocol.ToolCall != nil {
		protocol.ToolCall = nil
	}
	if protocol.Method == ProtocolMethodSessionUpdate && protocol.Update == nil && protocol.Permission == nil {
		event.Protocol = nil
		return
	}
	event.Protocol = &protocol
}
