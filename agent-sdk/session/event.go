package session

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// EventNotice is the structured notice payload for one transient notice event.
type EventNotice struct {
	Level string         `json:"level,omitempty"`
	Text  string         `json:"text,omitempty"`
	Kind  string         `json:"kind,omitempty"`
	Meta  map[string]any `json:"meta,omitempty"`
}

// EventLifecycle is the structured lifecycle payload for one runtime event.
type EventLifecycle struct {
	Status string         `json:"status,omitempty"`
	Reason string         `json:"reason,omitempty"`
	Meta   map[string]any `json:"meta,omitempty"`
}

// EventTool is the durable SDK tool-execution payload for one tool call or
// result event. ACP wire shapes are derived from this payload by surface
// projectors; they are not the storage contract.
type EventTool struct {
	ID        string              `json:"id,omitempty"`
	Name      string              `json:"name,omitempty"`
	Kind      string              `json:"kind,omitempty"`
	Title     string              `json:"title,omitempty"`
	Status    string              `json:"status,omitempty"`
	Input     map[string]any      `json:"input,omitempty"`
	Output    map[string]any      `json:"output,omitempty"`
	Content   []EventToolContent  `json:"content,omitempty"`
	Locations []EventToolLocation `json:"locations,omitempty"`
}

// EventToolLocation points at one file location involved in a tool event.
type EventToolLocation struct {
	Path string `json:"path,omitempty"`
	Line *int   `json:"line,omitempty"`
}

// EventToolContent is durable display-oriented tool content. It intentionally
// avoids ACP's content envelope; ACP projectors map it to standard
// tool_call_update content and _meta terminal updates.
type EventToolContent struct {
	Type       string  `json:"type,omitempty"`
	Text       string  `json:"text,omitempty"`
	TerminalID string  `json:"terminal_id,omitempty"`
	Path       string  `json:"path,omitempty"`
	OldText    *string `json:"old_text,omitempty"`
	NewText    string  `json:"new_text,omitempty"`
}

// Event is the compact canonical event envelope. Durable model-visible
// messages live in Message. Durable tool execution state lives in Tool. Protocol
// is the ACP projection/control payload and must not be used as the local model
// replay source.
type Event struct {
	ID          string            `json:"id,omitempty"`
	SessionID   string            `json:"session_id,omitempty"`
	Seq         uint64            `json:"seq,omitempty"`
	Schema      int               `json:"schema,omitempty"`
	Type        EventType         `json:"type,omitempty"`
	Visibility  Visibility        `json:"visibility,omitempty"`
	Time        time.Time         `json:"time,omitempty"`
	Actor       ActorRef          `json:"actor,omitempty"`
	Scope       *EventScope       `json:"scope,omitempty"`
	Invocation  *EventInvocation  `json:"invocation,omitempty"`
	Message     *model.Message    `json:"message,omitempty"`
	Tool        *EventTool        `json:"tool,omitempty"`
	PlanPayload *EventPlanPayload `json:"plan,omitempty"`
	Notice      *EventNotice      `json:"notice,omitempty"`
	Lifecycle   *EventLifecycle   `json:"lifecycle,omitempty"`
	Protocol    *EventProtocol    `json:"protocol,omitempty"`
	Text        string            `json:"-"`
	Meta        map[string]any    `json:"_meta,omitempty"`
}

// NoticeOf returns the structured notice carried by one event, if any.
func NoticeOf(event *Event) (EventNotice, bool) {
	if event == nil {
		return EventNotice{}, false
	}
	if event.Notice != nil {
		out := *event.Notice
		out.Level = strings.TrimSpace(strings.ToLower(out.Level))
		out.Text = strings.TrimSpace(out.Text)
		out.Kind = strings.TrimSpace(out.Kind)
		out.Meta = cloneProtocolAnyMap(out.Meta)
		if out.Level != "" && out.Text != "" {
			return out, true
		}
	}
	return EventNotice{}, false
}

// MarkUIOnly annotates one event as UI-only.
func MarkUIOnly(event *Event) *Event {
	if event == nil {
		return nil
	}
	event.Visibility = VisibilityUIOnly
	if event.Type == "" {
		event.Type = EventTypeOf(event)
	}
	return event
}

// MarkOverlay annotates one event as transient display overlay state.
func MarkOverlay(event *Event) *Event {
	if event == nil {
		return nil
	}
	event.Visibility = VisibilityOverlay
	if event.Type == "" {
		event.Type = EventTypeOf(event)
	}
	return event
}

// MarkMirror annotates one event as durable transcript-only state.
func MarkMirror(event *Event) *Event {
	if event == nil {
		return nil
	}
	event.Visibility = VisibilityMirror
	if event.Type == "" {
		event.Type = EventTypeOf(event)
	}
	return event
}

// MarkNotice annotates one event as one transient runtime notice.
func MarkNotice(event *Event, level string, text string) *Event {
	if event == nil {
		return nil
	}
	event.Notice = &EventNotice{
		Level: strings.TrimSpace(strings.ToLower(level)),
		Text:  strings.TrimSpace(text),
	}
	event.Visibility = VisibilityUIOnly
	if event.Type == "" {
		event.Type = EventTypeNotice
	}
	return event
}

// IsUIOnly reports whether one event is UI-only.
func IsUIOnly(event *Event) bool {
	return event != nil && event.Visibility == VisibilityUIOnly
}

// IsOverlay reports whether one event is transient display overlay state.
func IsOverlay(event *Event) bool {
	return event != nil && event.Visibility == VisibilityOverlay
}

// IsMirror reports whether one event is transcript-only durable state.
func IsMirror(event *Event) bool {
	return event != nil && event.Visibility == VisibilityMirror
}

// IsNotice reports whether one event carries one structured notice.
func IsNotice(event *Event) bool {
	_, ok := NoticeOf(event)
	return ok
}

// EventText returns the display text carried by one event. Durable
// model-visible events should keep full Message payloads; ACP content remains a
// protocol projection source for protocol-native events.
func EventText(event *Event) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		if text := event.Message.TextContent(); text != "" {
			return text
		}
	}
	if event.Text != "" {
		return event.Text
	}
	if event.Notice != nil {
		if text := strings.TrimSpace(event.Notice.Text); text != "" {
			return text
		}
	}
	if update := ProtocolUpdateOf(event); update != nil {
		return textFromProtocolContent(update.Content)
	}
	return ""
}

// EventDisplayText returns the user-visible text for display-only consumers
// such as generated session titles. It preserves EventText's model-visible
// fallback order when no display override is present.
func EventDisplayText(event *Event) string {
	if event == nil {
		return ""
	}
	if event.Text != "" {
		return event.Text
	}
	return EventText(event)
}

// CanonicalizeEvent returns a normalized event copy. It preserves the canonical
// Message/Tool durable state and removes redundant ACP projection content when
// the canonical state already carries the same fact.
func CanonicalizeEvent(event *Event) *Event {
	out := CloneEvent(event)
	if out == nil {
		return nil
	}
	if out.Type == "" {
		out.Type = EventTypeOf(out)
	}
	if out.Tool != nil {
		removeToolProjectionProtocol(out)
	}
	if out.Message != nil {
		removeModelProjectionContent(out)
		return out
	}
	ensureProtocolText(out)
	return out
}
