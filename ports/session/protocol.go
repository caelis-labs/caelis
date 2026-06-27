package session

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// ProtocolUpdateType identifies one ACP-compatible protocol update family.
type ProtocolUpdateType string

const (
	ProtocolUpdateTypeUserMessage  ProtocolUpdateType = "user_message_chunk"
	ProtocolUpdateTypeAgentMessage ProtocolUpdateType = "agent_message_chunk"
	ProtocolUpdateTypeAgentThought ProtocolUpdateType = "agent_thought_chunk"
	ProtocolUpdateTypeToolCall     ProtocolUpdateType = "tool_call"
	ProtocolUpdateTypeToolUpdate   ProtocolUpdateType = "tool_call_update"
	ProtocolUpdateTypePlan         ProtocolUpdateType = "plan"
	ProtocolUpdateTypePermission   ProtocolUpdateType = "request_permission"
)

const (
	ProtocolMethodSessionUpdate     = "session/update"
	ProtocolMethodRequestPermission = "session/request_permission"
	ProtocolMethodParticipantUpdate = "caelis/participant"
	ProtocolMethodControllerHandoff = "caelis/handoff"
	ProtocolMethodRuntimeLifecycle  = "caelis/lifecycle"
	ProtocolMethodContextCheckpoint = "caelis/context_checkpoint"
)

// ProtocolToolCall is the ACP-compatible tool call or tool update view of one
// canonical event.
type ProtocolToolCall struct {
	ID        string                    `json:"id,omitempty"`
	Name      string                    `json:"name,omitempty"`
	Kind      string                    `json:"kind,omitempty"`
	Title     string                    `json:"title,omitempty"`
	Status    string                    `json:"status,omitempty"`
	RawInput  map[string]any            `json:"raw_input,omitempty"`
	RawOutput map[string]any            `json:"raw_output,omitempty"`
	Content   []ProtocolToolCallContent `json:"content,omitempty"`
}

// ProtocolToolCallLocation is the ACP tool-call location shape.
type ProtocolToolCallLocation struct {
	Path string `json:"path,omitempty"`
	Line *int   `json:"line,omitempty"`
}

// ProtocolToolCallContent is the ACP tool-call content shape.
type ProtocolToolCallContent struct {
	Type       string  `json:"type,omitempty"`
	Content    any     `json:"content,omitempty"`
	TerminalID string  `json:"terminalId,omitempty"`
	Path       string  `json:"path,omitempty"`
	OldText    *string `json:"oldText,omitempty"`
	NewText    string  `json:"newText,omitempty"`
}

// ProtocolUpdate is the normalized ACP session/update payload carried by one
// canonical event. Caelis-specific data belongs in Event.Meta["_meta"].caelis,
// not in this protocol payload.
type ProtocolUpdate struct {
	SessionUpdate string                     `json:"sessionUpdate,omitempty"`
	Content       any                        `json:"content,omitempty"`
	MessageID     string                     `json:"messageId,omitempty"`
	ToolCallID    string                     `json:"toolCallId,omitempty"`
	Title         string                     `json:"title,omitempty"`
	Kind          string                     `json:"kind,omitempty"`
	Status        string                     `json:"status,omitempty"`
	RawInput      map[string]any             `json:"rawInput,omitempty"`
	RawOutput     map[string]any             `json:"rawOutput,omitempty"`
	Locations     []ProtocolToolCallLocation `json:"locations,omitempty"`
	Entries       []ProtocolPlanEntry        `json:"entries,omitempty"`
	Meta          map[string]any             `json:"_meta,omitempty"`
}

// ProtocolTextContent returns the standard ACP text content payload.
func ProtocolTextContent(text string) map[string]any {
	if text == "" {
		return nil
	}
	return map[string]any{"type": "text", "text": text}
}

// ProtocolToolCallContentOf returns the ACP tool content array carried by a
// tool-call or tool-update protocol payload. Unsupported future shapes are
// discarded instead of being interpreted as display text.
func ProtocolToolCallContentOf(update *ProtocolUpdate) []ProtocolToolCallContent {
	if update == nil {
		return nil
	}
	return protocolToolCallContentFromAny(update.Content)
}

// CloneProtocolToolCallContent returns a deep copy of ACP tool-call content.
func CloneProtocolToolCallContent(in []ProtocolToolCallContent) []ProtocolToolCallContent {
	return cloneProtocolToolCallContents(in)
}

// ProtocolPlanEntry is one ACP-compatible plan row.
type ProtocolPlanEntry struct {
	Content  string `json:"content,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// ProtocolPlan is the ACP-compatible plan replacement payload.
type ProtocolPlan struct {
	Entries []ProtocolPlanEntry `json:"entries,omitempty"`
}

// ProtocolApprovalOption is one standard ACP permission option.
type ProtocolApprovalOption struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// ProtocolApproval is the ACP-compatible permission request payload. This maps
// to session/request_permission rather than inventing a kernel-only approval
// grammar.
type ProtocolApproval struct {
	ToolCall ProtocolToolCall         `json:"tool_call,omitempty"`
	Options  []ProtocolApprovalOption `json:"options,omitempty"`
}

// ProtocolParticipant is the participant lifecycle payload for one event.
type ProtocolParticipant struct {
	Action string `json:"action,omitempty"`
}

// ProtocolHandoff is the controller-handoff lifecycle payload for one event.
type ProtocolHandoff struct {
	Phase string `json:"phase,omitempty"`
}

// EventProtocol is the ACP-compatible protocol payload carried by one event.
// It groups protocol-shaped extensions under one nested object so Event itself
// stays small and stable.
type EventProtocol struct {
	Method      string               `json:"method,omitempty"`
	Update      *ProtocolUpdate      `json:"update,omitempty"`
	Permission  *ProtocolApproval    `json:"permission,omitempty"`
	UpdateType  string               `json:"-"`
	ToolCall    *ProtocolToolCall    `json:"-"`
	Plan        *ProtocolPlan        `json:"-"`
	Approval    *ProtocolApproval    `json:"-"`
	Participant *ProtocolParticipant `json:"-"`
	Handoff     *ProtocolHandoff     `json:"-"`
}

func (p EventProtocol) MarshalJSON() ([]byte, error) {
	type protocolJSON struct {
		Method     string            `json:"method,omitempty"`
		Update     *ProtocolUpdate   `json:"update,omitempty"`
		Permission *ProtocolApproval `json:"permission,omitempty"`
	}
	normalized := CloneEventProtocol(p)
	return json.Marshal(protocolJSON{
		Method:     normalized.Method,
		Update:     normalized.Update,
		Permission: normalized.Permission,
	})
}

func (p *EventProtocol) UnmarshalJSON(data []byte) error {
	type protocolJSON EventProtocol
	var decoded protocolJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	normalized := CloneEventProtocol(EventProtocol(decoded))
	*p = normalized
	return nil
}

// ProtocolUpdateOf returns the normalized ACP session/update payload for one
// event, accepting legacy in-memory aliases while keeping the durable JSON shape
// centered on EventProtocol.Update.
func ProtocolUpdateOf(event *Event) *ProtocolUpdate {
	if event == nil || event.Protocol == nil {
		return nil
	}
	protocol := CloneEventProtocol(*event.Protocol)
	return protocol.Update
}

func protocolContentIsText(content any, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" || content == nil {
		return false
	}
	return strings.TrimSpace(textFromProtocolContent(content)) == want
}

func protocolUpdateHasOnlySessionUpdate(update *ProtocolUpdate) bool {
	if update == nil {
		return true
	}
	return strings.TrimSpace(update.ToolCallID) == "" &&
		strings.TrimSpace(update.Title) == "" &&
		strings.TrimSpace(update.Kind) == "" &&
		strings.TrimSpace(update.Status) == "" &&
		strings.TrimSpace(update.MessageID) == "" &&
		update.Content == nil &&
		len(update.RawInput) == 0 &&
		len(update.RawOutput) == 0 &&
		len(update.Locations) == 0 &&
		len(update.Entries) == 0 &&
		len(update.Meta) == 0
}

func runtimeText(event *Event) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		if text := event.Message.TextContent(); text != "" {
			return text
		}
	}
	return event.Text
}

func textFromProtocolContent(content any) string {
	switch typed := content.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.RawMessage:
		if len(typed) == 0 {
			return ""
		}
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return string(typed)
		}
		return textFromProtocolContent(decoded)
	case map[string]any:
		if text, _ := typed["text"].(string); text != "" {
			return text
		}
		if text, _ := typed["content"].(string); text != "" {
			return text
		}
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := textFromProtocolContent(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func protocolToolCallContentFromAny(content any) []ProtocolToolCallContent {
	switch typed := content.(type) {
	case nil:
		return nil
	case []ProtocolToolCallContent:
		return cloneProtocolToolCallContents(typed)
	case json.RawMessage:
		if len(typed) == 0 {
			return nil
		}
		var decoded []ProtocolToolCallContent
		if err := json.Unmarshal(typed, &decoded); err == nil {
			return cloneProtocolToolCallContents(decoded)
		}
		var generic any
		if err := json.Unmarshal(typed, &generic); err != nil {
			return nil
		}
		return protocolToolCallContentFromAny(generic)
	case []any:
		out := make([]ProtocolToolCallContent, 0, len(typed))
		for _, item := range typed {
			if content, ok := protocolToolCallContentItemFromAny(item); ok {
				out = append(out, content)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		raw, err := json.Marshal(typed)
		if err != nil || len(raw) == 0 {
			return nil
		}
		var decoded []ProtocolToolCallContent
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil
		}
		return cloneProtocolToolCallContents(decoded)
	}
}

func protocolToolCallContentItemFromAny(item any) (ProtocolToolCallContent, bool) {
	switch typed := item.(type) {
	case nil:
		return ProtocolToolCallContent{}, false
	case ProtocolToolCallContent:
		cloned := cloneProtocolToolCallContents([]ProtocolToolCallContent{typed})
		return cloned[0], true
	case map[string]any:
		out := ProtocolToolCallContent{
			Type:       strings.TrimSpace(protocolContentString(typed["type"])),
			Content:    cloneProtocolAny(typed["content"]),
			TerminalID: strings.TrimSpace(protocolContentString(typed["terminalId"])),
			Path:       strings.TrimSpace(protocolContentString(typed["path"])),
		}
		out.NewText, _ = protocolRawString(typed["newText"])
		if out.TerminalID == "" {
			out.TerminalID = strings.TrimSpace(protocolContentString(typed["terminal_id"]))
		}
		if out.NewText == "" {
			out.NewText, _ = protocolRawString(typed["new_text"])
		}
		if oldText, ok := protocolOptionalString(typed["oldText"]); ok {
			out.OldText = &oldText
		} else if oldText, ok := protocolOptionalString(typed["old_text"]); ok {
			out.OldText = &oldText
		}
		return out, out.Type != "" || out.Content != nil || out.TerminalID != "" || out.Path != "" || out.OldText != nil || out.NewText != ""
	default:
		raw, err := json.Marshal(typed)
		if err != nil || len(raw) == 0 {
			return ProtocolToolCallContent{}, false
		}
		var decoded ProtocolToolCallContent
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return ProtocolToolCallContent{}, false
		}
		cloned := cloneProtocolToolCallContents([]ProtocolToolCallContent{decoded})
		return cloned[0], cloned[0].Type != "" || cloned[0].Content != nil || cloned[0].TerminalID != "" || cloned[0].Path != "" || cloned[0].OldText != nil || cloned[0].NewText != ""
	}
}

func protocolContentString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func protocolRawString(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func protocolOptionalString(value any) (string, bool) {
	return protocolRawString(value)
}

// CloneEventProtocol returns one normalized event protocol payload copy.
func CloneEventProtocol(in EventProtocol) EventProtocol {
	out := EventProtocol{
		Method:     strings.TrimSpace(in.Method),
		UpdateType: strings.TrimSpace(in.UpdateType),
	}
	var sourceToolCall *ProtocolToolCall
	if in.ToolCall != nil {
		call := cloneProtocolToolCall(*in.ToolCall)
		sourceToolCall = &call
	}
	if in.Update != nil {
		update := cloneProtocolUpdate(*in.Update)
		out.Update = &update
	}
	if out.Update == nil {
		switch {
		case sourceToolCall != nil:
			call := *sourceToolCall
			update := &ProtocolUpdate{
				SessionUpdate: firstNonEmpty(out.UpdateType, string(ProtocolUpdateTypeToolCall)),
				ToolCallID:    call.ID,
				Title:         call.Title,
				Kind:          firstNonEmpty(call.Kind, call.Name),
				Status:        call.Status,
				RawInput:      maps.Clone(call.RawInput),
				RawOutput:     maps.Clone(call.RawOutput),
			}
			if len(call.Content) > 0 {
				update.Content = cloneProtocolToolCallContents(call.Content)
			}
			out.Update = update
		case in.Plan != nil:
			out.Update = &ProtocolUpdate{
				SessionUpdate: firstNonEmpty(out.UpdateType, string(ProtocolUpdateTypePlan)),
				Entries:       cloneProtocolPlanEntries(in.Plan.Entries),
			}
		}
	}
	if out.Update != nil {
		update := cloneProtocolUpdate(*out.Update)
		if update.SessionUpdate == "" {
			update.SessionUpdate = out.UpdateType
		}
		out.UpdateType = strings.TrimSpace(update.SessionUpdate)
		out.Update = &update
		switch update.SessionUpdate {
		case string(ProtocolUpdateTypeToolCall), string(ProtocolUpdateTypeToolUpdate):
			sourceName := ""
			sourceKind := ""
			sourceTitle := ""
			if sourceToolCall != nil {
				sourceName = sourceToolCall.Name
				sourceKind = sourceToolCall.Kind
				sourceTitle = sourceToolCall.Title
			}
			out.ToolCall = &ProtocolToolCall{
				ID:        strings.TrimSpace(update.ToolCallID),
				Name:      firstNonEmpty(sourceName, strings.TrimSpace(update.Kind), strings.TrimSpace(update.Title)),
				Kind:      firstNonEmpty(strings.TrimSpace(update.Kind), sourceKind),
				Title:     firstNonEmpty(strings.TrimSpace(update.Title), sourceTitle),
				Status:    strings.TrimSpace(update.Status),
				RawInput:  maps.Clone(update.RawInput),
				RawOutput: maps.Clone(update.RawOutput),
				Content:   ProtocolToolCallContentOf(&update),
			}
		case string(ProtocolUpdateTypePlan):
			out.Plan = &ProtocolPlan{Entries: cloneProtocolPlanEntries(update.Entries)}
		}
	}
	if in.Permission != nil {
		approval := cloneProtocolApproval(*in.Permission)
		out.Permission = &approval
		out.Approval = &approval
	}
	if in.Approval != nil && out.Permission == nil {
		approval := cloneProtocolApproval(*in.Approval)
		out.Permission = &approval
		out.Approval = &approval
	}
	if out.Method == "" {
		switch {
		case out.Permission != nil:
			out.Method = ProtocolMethodRequestPermission
		default:
			out.Method = ProtocolMethodSessionUpdate
		}
	}
	if in.Participant != nil {
		participant := *in.Participant
		participant.Action = strings.TrimSpace(participant.Action)
		out.Participant = &participant
		if out.Update == nil {
			out.Method = ProtocolMethodParticipantUpdate
			out.Update = &ProtocolUpdate{
				SessionUpdate: strings.TrimSpace(participant.Action),
			}
		}
	}
	if in.Handoff != nil {
		handoff := *in.Handoff
		handoff.Phase = strings.TrimSpace(handoff.Phase)
		out.Handoff = &handoff
		if out.Update == nil {
			out.Method = ProtocolMethodControllerHandoff
			out.Update = &ProtocolUpdate{
				SessionUpdate: strings.TrimSpace(handoff.Phase),
			}
		}
	}
	return out
}

func cloneProtocolUpdate(in ProtocolUpdate) ProtocolUpdate {
	out := ProtocolUpdate{
		SessionUpdate: strings.TrimSpace(in.SessionUpdate),
		Content:       cloneProtocolAny(in.Content),
		MessageID:     strings.TrimSpace(in.MessageID),
		ToolCallID:    strings.TrimSpace(in.ToolCallID),
		Title:         strings.TrimSpace(in.Title),
		Kind:          strings.TrimSpace(in.Kind),
		Status:        strings.TrimSpace(in.Status),
		RawInput:      maps.Clone(in.RawInput),
		RawOutput:     maps.Clone(in.RawOutput),
		Meta:          cloneProtocolAnyMap(in.Meta),
	}
	if len(in.Locations) > 0 {
		out.Locations = slices.Clone(in.Locations)
	}
	out.Entries = cloneProtocolPlanEntries(in.Entries)
	return out
}

func cloneProtocolPlanEntries(in []ProtocolPlanEntry) []ProtocolPlanEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]ProtocolPlanEntry, 0, len(in))
	for _, item := range in {
		out = append(out, ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: strings.TrimSpace(item.Priority),
		})
	}
	return out
}

func cloneProtocolToolCallContents(in []ProtocolToolCallContent) []ProtocolToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]ProtocolToolCallContent, 0, len(in))
	for _, item := range in {
		var oldText *string
		if item.OldText != nil {
			value := *item.OldText
			oldText = &value
		}
		out = append(out, ProtocolToolCallContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    cloneProtocolAny(item.Content),
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    oldText,
			NewText:    item.NewText,
		})
	}
	return out
}

func cloneProtocolApproval(in ProtocolApproval) ProtocolApproval {
	out := ProtocolApproval{
		ToolCall: cloneProtocolToolCall(in.ToolCall),
	}
	if len(in.Options) > 0 {
		out.Options = make([]ProtocolApprovalOption, 0, len(in.Options))
		for _, item := range in.Options {
			out.Options = append(out.Options, ProtocolApprovalOption{
				ID:   strings.TrimSpace(item.ID),
				Name: strings.TrimSpace(item.Name),
				Kind: strings.TrimSpace(item.Kind),
			})
		}
	}
	return out
}

func cloneProtocolAny(in any) any {
	switch typed := in.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return slices.Clone(typed)
	case map[string]any:
		return cloneProtocolAnyMap(typed)
	case []ProtocolToolCallContent:
		if len(typed) == 0 {
			return nil
		}
		return cloneProtocolToolCallContents(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneProtocolAny(item))
		}
		return out
	default:
		return typed
	}
}

func cloneProtocolAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneProtocolAny(value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func cloneProtocolToolCall(in ProtocolToolCall) ProtocolToolCall {
	call := in
	call.ID = strings.TrimSpace(call.ID)
	call.Name = strings.TrimSpace(call.Name)
	call.Kind = strings.TrimSpace(call.Kind)
	call.Title = strings.TrimSpace(call.Title)
	call.Status = strings.TrimSpace(call.Status)
	call.RawInput = maps.Clone(call.RawInput)
	call.RawOutput = maps.Clone(call.RawOutput)
	call.Content = cloneProtocolToolCallContents(call.Content)
	return call
}
