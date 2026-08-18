package runtime

import (
	"iter"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

// liveContentOwnership records only which logical Assistant message already
// has a live delta source. It never compares or merges content.
type liveContentOwnership struct {
	messages map[liveContentKey]agentsdk.PublishedContent
}

type liveContentKey struct {
	participantID string
	delegationID  string
	acpSessionID  string
	turnID        string
	messageID     string
}

func (o *liveContentOwnership) observe(event *session.Event) agentsdk.PublishedContent {
	if event == nil {
		return 0
	}
	if session.IsUIOnly(event) {
		published := liveNarrativeContent(event)
		if published == 0 {
			return 0
		}
		if messageID := strings.TrimSpace(session.EventMessageID(event)); messageID != "" {
			if o.messages == nil {
				o.messages = map[liveContentKey]agentsdk.PublishedContent{}
			}
			key := liveEventContentKey(event, messageID)
			o.messages[key] |= published
		}
		return 0
	}

	var alreadyPublished agentsdk.PublishedContent
	if taskStreamOwnsTerminalContent(event) {
		alreadyPublished |= agentsdk.PublishedTerminal
	}
	if !session.IsCanonicalHistoryEvent(event) {
		return alreadyPublished
	}
	canonical := canonicalNarrativeContent(event)
	if canonical == 0 {
		return alreadyPublished
	}
	if messageID := strings.TrimSpace(session.EventMessageID(event)); messageID != "" {
		key := liveEventContentKey(event, messageID)
		alreadyPublished |= o.messages[key] & canonical
		delete(o.messages, key)
		return alreadyPublished
	}
	return alreadyPublished
}

// SourceEvents adapts either Runner event view to the explicit source contract.
// Events-only implementations use the same content-ownership algorithm as the
// built-in Runtime runner; callers still consume exactly one underlying view.
func SourceEvents(handle agentsdk.Runner) iter.Seq2[agentsdk.SourceEvent, error] {
	if source, ok := handle.(agentsdk.SourceHandle); ok && source != nil {
		return source.SourceEvents()
	}
	if handle == nil {
		return SourceEventsFromEvents(nil)
	}
	return SourceEventsFromEvents(handle.Events())
}

// SourceEventsFromEvents applies explicit content ownership while adapting one
// Events-only stream. Product bridges use this for legacy handle shapes that do
// not expose the full Runner contract.
func SourceEventsFromEvents(events iter.Seq2[*session.Event, error]) iter.Seq2[agentsdk.SourceEvent, error] {
	return func(yield func(agentsdk.SourceEvent, error) bool) {
		if events == nil {
			return
		}
		ownership := liveContentOwnership{}
		for event, err := range events {
			if err != nil {
				if !yield(agentsdk.SourceEvent{}, err) {
					return
				}
				continue
			}
			if !yield(agentsdk.SourceEvent{
				Canonical:                        session.CloneEvent(event),
				CanonicalContentAlreadyPublished: ownership.observe(event),
			}, nil) {
				return
			}
		}
	}
}

func liveEventContentKey(event *session.Event, messageID string) liveContentKey {
	key := liveContentKey{messageID: strings.TrimSpace(messageID)}
	if event == nil || event.Scope == nil {
		return key
	}
	key.participantID = strings.TrimSpace(event.Scope.Participant.ID)
	key.delegationID = strings.TrimSpace(event.Scope.Participant.DelegationID)
	key.acpSessionID = strings.TrimSpace(event.Scope.ACP.SessionID)
	key.turnID = strings.TrimSpace(event.Scope.TurnID)
	return key
}

func liveNarrativeContent(event *session.Event) agentsdk.PublishedContent {
	if event == nil {
		return 0
	}
	updateType := strings.TrimSpace(session.ProtocolSessionUpdateType(event))
	switch updateType {
	case string(session.ProtocolUpdateTypeAgentMessage):
		return agentsdk.PublishedAssistantMessage
	case string(session.ProtocolUpdateTypeAgentThought):
		return agentsdk.PublishedAssistantThought
	}
	return narrativeContentFromMessage(event)
}

func canonicalNarrativeContent(event *session.Event) agentsdk.PublishedContent {
	if event == nil {
		return 0
	}
	published := narrativeContentFromMessage(event)
	switch strings.TrimSpace(session.ProtocolSessionUpdateType(event)) {
	case string(session.ProtocolUpdateTypeAgentMessage):
		published |= agentsdk.PublishedAssistantMessage
	case string(session.ProtocolUpdateTypeAgentThought):
		published |= agentsdk.PublishedAssistantThought
	}
	return published
}

func narrativeContentFromMessage(event *session.Event) agentsdk.PublishedContent {
	message := event.Message
	if message == nil {
		if projected, ok := session.ModelMessageOf(event); ok {
			message = &projected
		}
	}
	if message == nil || message.Role != model.RoleAssistant {
		return 0
	}
	var published agentsdk.PublishedContent
	if strings.TrimSpace(message.TextContent()) != "" {
		published |= agentsdk.PublishedAssistantMessage
	}
	if strings.TrimSpace(message.ReasoningText()) != "" {
		published |= agentsdk.PublishedAssistantThought
	}
	return published
}

func taskStreamOwnsTerminalContent(event *session.Event) bool {
	if event == nil || event.Tool == nil {
		return false
	}
	if !taskRuntimeMetaBool(event.Meta, toolbinding.MetadataSection, toolbinding.MetadataTaskResult) {
		return false
	}
	if strings.TrimSpace(taskRuntimeMetaString(event.Meta, "task", "task_id")) == "" {
		return false
	}
	targetKind := firstNonEmpty(
		taskRuntimeMetaString(event.Meta, "tool", "target_kind"),
		taskRuntimeMetaString(event.Meta, "task", "kind"),
	)
	return strings.EqualFold(strings.TrimSpace(targetKind), string(taskapi.KindCommand))
}
