package session

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
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
	// ProtocolMethodAgentCommunication is retained only to read events written
	// before Agent communication moved onto standard session/update messages.
	ProtocolMethodAgentCommunication = "caelis/agent_communication"
)

const AgentCommunicationMetaKey = "agent_communication"

// ProtocolToolCall is the ACP-compatible tool call or tool update view of one
// canonical event.
type ProtocolToolCall struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// NamePresent records external name presence: true includes an empty name,
	// false keeps a compatibility-derived Name out of the standard wire field.
	// Nil uses a non-empty Name for native approvals and older stored records.
	NamePresent *bool                     `json:"name_present,omitempty"`
	Kind        string                    `json:"kind,omitempty"`
	Title       string                    `json:"title,omitempty"`
	Status      string                    `json:"status,omitempty"`
	RawInput    map[string]any            `json:"raw_input,omitempty"`
	RawOutput   map[string]any            `json:"raw_output,omitempty"`
	Content     []ProtocolToolCallContent `json:"content,omitempty"`
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
	// Changes and Patch preserve structured diffs. Legacy OldText/NewText keep
	// their whole-file meaning; a rendered patch is never assigned to NewText.
	Changes *[]ProtocolDiffChange `json:"changes,omitempty"`
	Patch   *ProtocolDiffPatch    `json:"patch,omitempty"`
	Meta    map[string]any        `json:"_meta,omitempty"`
}

// ProtocolUpdate is the normalized ACP session/update payload carried by one
// canonical event. Caelis-specific projection data belongs under Meta["caelis"]
// and is serialized as the standard payload's private _meta field.
// MessageID and ToolCallID are opaque Session identities; preserve exact bytes.
// Name follows ACP v1: absent or null leaves the current tool name unchanged,
// unless normalized display patch presence explicitly includes "name".
type ProtocolUpdate struct {
	SessionUpdate string `json:"sessionUpdate,omitempty"`
	Content       any    `json:"content,omitempty"`
	MessageID     string `json:"messageId,omitempty"`
	// Replacement replaces the identified narrative, including with empty text.
	// It does not settle its Turn or grant execution authority.
	Replacement bool `json:"replacement,omitempty"`
	// PreviouslyObserved is a process-local display observation from the Session
	// narrative/tool owner. It cannot be supplied by a peer or recovered from storage.
	PreviouslyObserved bool `json:"-"`
	// ReplacedFields identifies present display fields, including null or empty
	// values. Absent fields retain their current value; this grants no authority.
	ReplacedFields []string                   `json:"replacedFields,omitempty"`
	ToolCallID     string                     `json:"toolCallId,omitempty"`
	Name           *string                    `json:"name,omitempty"`
	Title          string                     `json:"title,omitempty"`
	Kind           string                     `json:"kind,omitempty"`
	Status         string                     `json:"status,omitempty"`
	RawInput       map[string]any             `json:"rawInput,omitempty"`
	RawOutput      map[string]any             `json:"rawOutput,omitempty"`
	Locations      []ProtocolToolCallLocation `json:"locations,omitempty"`
	Entries        []ProtocolPlanEntry        `json:"entries,omitempty"`
	Meta           map[string]any             `json:"_meta,omitempty"`
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
	// Subject is normalized SDK display evidence, outside standard ACP fields.
	Subject *ApprovalSubject `json:"subject,omitempty"`
	// Context is independent permission evidence for endpoints whose request
	// does not necessarily refer to a tool call.
	Context *PermissionContext `json:"context,omitempty"`
}

// ProtocolParticipant is the participant lifecycle payload for one event.
type ProtocolParticipant struct {
	Action string `json:"action,omitempty"`
}

// ProtocolHandoff is the controller-handoff lifecycle payload for one event.
type ProtocolHandoff struct {
	Phase string `json:"phase,omitempty"`
}

// ProtocolAgentCommunication carries the display text of one trusted
// Agent-to-Agent message. The durable Event.Actor remains the sender identity.
type ProtocolAgentCommunication struct {
	Text string `json:"text,omitempty"`
}

// NewParticipantProtocol constructs the normalized participant lifecycle
// protocol payload shared by built-in and external ACP adapters.
func NewParticipantProtocol(participant ProtocolParticipant) EventProtocol {
	return CloneEventProtocol(EventProtocol{
		Method: ProtocolMethodParticipantUpdate,
		Update: &ProtocolUpdate{SessionUpdate: strings.TrimSpace(participant.Action)},
	})
}

// NewHandoffProtocol constructs an already-authorized controller handoff fact.
// Constructing the payload does not grant handoff authority; Control remains
// responsible for committing controller transfer.
func NewHandoffProtocol(handoff ProtocolHandoff) EventProtocol {
	return CloneEventProtocol(EventProtocol{
		Method: ProtocolMethodControllerHandoff,
		Update: &ProtocolUpdate{SessionUpdate: strings.TrimSpace(handoff.Phase)},
	})
}

// NewAgentCommunicationProtocol constructs the standard ACP user-message
// projection for one Agent-to-Agent input. It does not grant sender identity;
// callers must supply the trusted source through Event.Actor.
func NewAgentCommunicationProtocol(communication ProtocolAgentCommunication) EventProtocol {
	return CloneEventProtocol(EventProtocol{
		Method: ProtocolMethodSessionUpdate,
		Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeUserMessage),
			Content:       ProtocolTextContent(strings.TrimSpace(communication.Text)),
			Meta: map[string]any{
				"caelis": map[string]any{AgentCommunicationMetaKey: map[string]any{}},
			},
		},
	})
}

// EventProtocol is the ACP-compatible protocol payload carried by one event.
// It groups protocol-shaped extensions under one nested object so Event itself
// stays small and stable.
type EventProtocol struct {
	Method             string                      `json:"method,omitempty"`
	Update             *ProtocolUpdate             `json:"update,omitempty"`
	Permission         *ProtocolApproval           `json:"permission,omitempty"`
	AgentCommunication *ProtocolAgentCommunication `json:"agent_communication,omitempty"`
}

func (p EventProtocol) MarshalJSON() ([]byte, error) {
	type protocolJSON struct {
		Method             string                      `json:"method,omitempty"`
		Update             *ProtocolUpdate             `json:"update,omitempty"`
		Permission         *ProtocolApproval           `json:"permission,omitempty"`
		AgentCommunication *ProtocolAgentCommunication `json:"agent_communication,omitempty"`
	}
	normalized := CloneEventProtocol(p)
	return json.Marshal(protocolJSON(normalized))
}

func (p *EventProtocol) UnmarshalJSON(data []byte) error {
	type protocolJSON EventProtocol
	var decoded protocolJSON
	wire := struct {
		*protocolJSON
		Update json.RawMessage `json:"update"`
	}{protocolJSON: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire.Update) > 0 && !bytes.Equal(bytes.TrimSpace(wire.Update), []byte("null")) {
		var update ProtocolUpdate
		fields := struct {
			*ProtocolUpdate
			Meta    json.RawMessage `json:"_meta"`
			Content json.RawMessage `json:"content"`
		}{ProtocolUpdate: &update}
		if err := json.Unmarshal(wire.Update, &fields); err != nil {
			return err
		}
		// Never route opaque metadata through float64, even temporarily:
		// a valid numeric token may exceed float64's finite range.
		if len(fields.Meta) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(fields.Meta))
			decoder.UseNumber()
			if err := decoder.Decode(&update.Meta); err != nil {
				return err
			}
		}
		// Structured content can now carry patch annotations with exact JSON
		// numbers. Decode the generic content before canonical typed restoration.
		if len(fields.Content) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(fields.Content))
			decoder.UseNumber()
			if err := decoder.Decode(&update.Content); err != nil {
				return err
			}
		}
		decoded.Update = &update
	}
	*p = CloneEventProtocol(EventProtocol(decoded))
	return nil
}

// ProtocolUpdateOf returns the normalized ACP session/update payload for one
// event.
func ProtocolUpdateOf(event *Event) *ProtocolUpdate {
	if event == nil || event.Protocol == nil {
		return nil
	}
	return ProtocolUpdateOfProtocol(event.Protocol)
}

// ProtocolUpdateOfProtocol returns the normalized ACP session/update payload
// for one protocol payload.
func ProtocolUpdateOfProtocol(protocol *EventProtocol) *ProtocolUpdate {
	if protocol == nil {
		return nil
	}
	normalized := CloneEventProtocol(*protocol)
	return normalized.Update
}

// ProtocolSessionUpdateType returns the normalized ACP session/update type for
// one event.
func ProtocolSessionUpdateType(event *Event) string {
	if update := ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.SessionUpdate)
	}
	return ""
}

// ProtocolSessionUpdateTypeOfProtocol returns the normalized ACP session/update
// type for one protocol payload.
func ProtocolSessionUpdateTypeOfProtocol(protocol *EventProtocol) string {
	if update := ProtocolUpdateOfProtocol(protocol); update != nil {
		return strings.TrimSpace(update.SessionUpdate)
	}
	return ""
}

// ProtocolPermissionOf returns the normalized ACP permission payload for one
// event.
func ProtocolPermissionOf(event *Event) *ProtocolApproval {
	if event == nil || event.Protocol == nil {
		return nil
	}
	normalized := CloneEventProtocol(*event.Protocol)
	if normalized.Permission == nil {
		return nil
	}
	out := cloneProtocolApproval(*normalized.Permission)
	return &out
}

// ProtocolParticipantOf returns the normalized ACP participant payload for one
// event.
func ProtocolParticipantOf(event *Event) *ProtocolParticipant {
	if event == nil || event.Protocol == nil {
		return nil
	}
	normalized := CloneEventProtocol(*event.Protocol)
	if normalized.Method != ProtocolMethodParticipantUpdate || normalized.Update == nil {
		return nil
	}
	out := ProtocolParticipant{Action: strings.TrimSpace(normalized.Update.SessionUpdate)}
	if out.Action == "" {
		return nil
	}
	return &out
}

// ProtocolHandoffOf returns the normalized ACP controller-handoff payload for
// one event.
func ProtocolHandoffOf(event *Event) *ProtocolHandoff {
	if event == nil || event.Protocol == nil {
		return nil
	}
	normalized := CloneEventProtocol(*event.Protocol)
	if normalized.Method != ProtocolMethodControllerHandoff || normalized.Update == nil {
		return nil
	}
	out := ProtocolHandoff{Phase: strings.TrimSpace(normalized.Update.SessionUpdate)}
	if out.Phase == "" {
		return nil
	}
	return &out
}

// ProtocolAgentCommunicationOf returns the Agent communication projection
// carried by one event. New events use a standard ACP user-message update;
// the custom protocol method remains readable for durable compatibility.
func ProtocolAgentCommunicationOf(event *Event) *ProtocolAgentCommunication {
	if !IsAgentCommunicationProtocol(event) {
		return nil
	}
	normalized := CloneEventProtocol(*event.Protocol)
	if normalized.Method == ProtocolMethodAgentCommunication && normalized.AgentCommunication != nil {
		out := *normalized.AgentCommunication
		if strings.TrimSpace(out.Text) == "" {
			return nil
		}
		return &out
	}
	if normalized.Method != ProtocolMethodSessionUpdate || normalized.Update == nil ||
		strings.TrimSpace(normalized.Update.SessionUpdate) != string(ProtocolUpdateTypeUserMessage) ||
		!protocolUpdateHasAgentCommunicationMeta(normalized.Update) {
		return nil
	}
	text := textFromProtocolContent(normalized.Update.Content)
	observation := IsUIOnly(event) || IsMirror(event)
	if observation && normalized.Update.Content == nil && event.Message != nil {
		// Storage may remove a duplicate display projection while preserving
		// its observed Message. Explicit empty content never uses this path.
		text = event.Message.TextContent()
	}
	if !observation {
		text = strings.TrimSpace(text)
	}
	if text == "" {
		text = event.Text
		if !observation {
			text = strings.TrimSpace(text)
		}
	}
	if text == "" {
		// Empty observed patches repair display only. Canonical admitted mail
		// retains its nonempty text and actor validation requirements.
		if !observation || EventMessageID(event) == "" ||
			(!normalized.Update.Replacement && !slices.Contains(normalized.Update.ReplacedFields, "_meta")) {
			return nil
		}
	}
	return &ProtocolAgentCommunication{Text: text}
}

// IsAgentCommunicationProtocol reports whether an event declares the standard
// Agent-communication marker or the legacy custom protocol method. It also
// reports malformed declarations on typed Context events so validation can
// reject them. Private ACP metadata alone cannot turn user input into Context.
func IsAgentCommunicationProtocol(event *Event) bool {
	if event == nil || event.Protocol == nil {
		return false
	}
	normalized := CloneEventProtocol(*event.Protocol)
	if normalized.Method == ProtocolMethodAgentCommunication {
		return true
	}
	return event.Type == EventTypeContext &&
		normalized.Method == ProtocolMethodSessionUpdate && normalized.Update != nil &&
		strings.TrimSpace(normalized.Update.SessionUpdate) == string(ProtocolUpdateTypeUserMessage) &&
		protocolUpdateHasAgentCommunicationMeta(normalized.Update)
}

func protocolUpdateHasAgentCommunicationMeta(update *ProtocolUpdate) bool {
	if update == nil {
		return false
	}
	caelis, ok := update.Meta["caelis"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = caelis[AgentCommunicationMetaKey]
	return ok
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
	return update.ToolCallID == "" &&
		update.Name == nil &&
		strings.TrimSpace(update.Title) == "" &&
		strings.TrimSpace(update.Kind) == "" &&
		strings.TrimSpace(update.Status) == "" &&
		update.MessageID == "" &&
		!update.Replacement &&
		len(update.ReplacedFields) == 0 &&
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
		if raw, err := json.Marshal(typed); err == nil {
			var structured ProtocolToolCallContent
			if json.Unmarshal(raw, &structured) == nil {
				out.Changes = CloneProtocolDiffChanges(structured.Changes)
				out.Patch = CloneProtocolDiffPatch(structured.Patch)
				out.Meta = CloneState(structured.Meta)
			}
		}
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
		Method: strings.TrimSpace(in.Method),
	}
	if in.Update != nil {
		update := cloneProtocolUpdate(*in.Update)
		out.Update = &update
	}
	if in.Permission != nil {
		approval := cloneProtocolApproval(*in.Permission)
		out.Permission = &approval
	}
	if in.AgentCommunication != nil {
		communication := *in.AgentCommunication
		communication.Text = strings.TrimSpace(communication.Text)
		out.AgentCommunication = &communication
	}
	if out.Method == "" {
		switch {
		case out.AgentCommunication != nil:
			out.Method = ProtocolMethodAgentCommunication
		case out.Permission != nil:
			out.Method = ProtocolMethodRequestPermission
		case out.Update != nil:
			out.Method = ProtocolMethodSessionUpdate
		}
	}
	return out
}

func cloneProtocolUpdate(in ProtocolUpdate) ProtocolUpdate {
	out := ProtocolUpdate{
		SessionUpdate:      strings.TrimSpace(in.SessionUpdate),
		Content:            cloneProtocolAny(in.Content),
		MessageID:          in.MessageID,
		Replacement:        in.Replacement,
		PreviouslyObserved: in.PreviouslyObserved,
		ReplacedFields:     slices.Clone(in.ReplacedFields),
		ToolCallID:         in.ToolCallID,
		Title:              strings.TrimSpace(in.Title),
		Kind:               strings.TrimSpace(in.Kind),
		Status:             strings.TrimSpace(in.Status),
		RawInput:           jsonvalue.CloneMap(in.RawInput),
		RawOutput:          jsonvalue.CloneMap(in.RawOutput),
		Meta:               cloneProtocolAnyMap(in.Meta),
	}
	if in.Name != nil {
		name := *in.Name
		out.Name = &name
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
	if in == nil {
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
			Changes:    CloneProtocolDiffChanges(item.Changes),
			Patch:      CloneProtocolDiffPatch(item.Patch),
			Meta:       CloneState(item.Meta),
		})
	}
	return out
}

func cloneProtocolApproval(in ProtocolApproval) ProtocolApproval {
	out := ProtocolApproval{
		ToolCall: cloneProtocolToolCall(in.ToolCall),
		Subject:  CloneApprovalSubject(in.Subject),
		Context:  ClonePermissionContext(in.Context),
	}
	if in.Context != nil {
		out.ToolCall.ID, out.ToolCall.Name, out.ToolCall.Kind, out.ToolCall.Title, out.ToolCall.Status = in.ToolCall.ID, in.ToolCall.Name, in.ToolCall.Kind, in.ToolCall.Title, in.ToolCall.Status
	}
	if len(in.Options) > 0 {
		out.Options = make([]ProtocolApprovalOption, 0, len(in.Options))
		for _, item := range in.Options {
			out.Options = append(out.Options, ProtocolApprovalOption{
				ID:   strings.TrimSpace(item.ID),
				Name: strings.TrimSpace(item.Name),
				Kind: strings.TrimSpace(item.Kind),
			})
			if in.Context != nil {
				out.Options[len(out.Options)-1].Name = item.Name
			}
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
		return cloneProtocolToolCallContents(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneProtocolAny(item))
		}
		return out
	default:
		return jsonvalue.Clone(typed)
	}
}

func cloneProtocolAnyMap(in map[string]any) map[string]any {
	return jsonvalue.CloneMap(in)
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
	if in.NamePresent != nil {
		call.NamePresent = new(*in.NamePresent)
	}
	if call.NamePresent == nil || !*call.NamePresent {
		call.Name = strings.TrimSpace(call.Name)
	}
	call.Kind = strings.TrimSpace(call.Kind)
	call.Title = strings.TrimSpace(call.Title)
	call.Status = strings.TrimSpace(call.Status)
	call.RawInput = jsonvalue.CloneMap(call.RawInput)
	call.RawOutput = jsonvalue.CloneMap(call.RawOutput)
	call.Content = cloneProtocolToolCallContents(call.Content)
	return call
}

// CloneProtocolApproval returns one recursively isolated approval payload.
func CloneProtocolApproval(in ProtocolApproval) ProtocolApproval {
	return cloneProtocolApproval(in)
}
