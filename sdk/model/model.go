package model

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
)

// Role identifies message author type.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentPartType identifies one prompt-side multimodal input unit.
type ContentPartType string

const (
	ContentPartText  ContentPartType = "text"
	ContentPartImage ContentPartType = "image"
)

// ContentPart is one user-facing prompt content unit.
type ContentPart struct {
	Type     ContentPartType `json:"type"`
	Text     string          `json:"text,omitempty"`
	MimeType string          `json:"mime_type,omitempty"`
	Data     string          `json:"data,omitempty"`
	FileName string          `json:"file_name,omitempty"`
}

// ToolCall represents one model-emitted tool invocation.
type ToolCall struct {
	ID               string `json:"id,omitempty"`
	Name             string `json:"name,omitempty"`
	Args             string `json:"args,omitempty"`
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// ToolResponse represents one tool result fed back to the model.
type ToolResponse struct {
	ID     string         `json:"id,omitempty"`
	Name   string         `json:"name,omitempty"`
	Result map[string]any `json:"result,omitempty"`
}

// PartKind identifies one provider-neutral message part.
type PartKind string

const (
	PartKindText       PartKind = "text"
	PartKindReasoning  PartKind = "reasoning"
	PartKindToolUse    PartKind = "tool_use"
	PartKindToolResult PartKind = "tool_result"
	PartKindMedia      PartKind = "media"
	PartKindJSON       PartKind = "json"
	PartKindFileRef    PartKind = "file_ref"
)

// ReasoningVisibility indicates how reasoning content may be surfaced.
type ReasoningVisibility string

const (
	ReasoningVisibilityVisible   ReasoningVisibility = "visible"
	ReasoningVisibilityHidden    ReasoningVisibility = "hidden"
	ReasoningVisibilityRedacted  ReasoningVisibility = "redacted"
	ReasoningVisibilityTokenOnly ReasoningVisibility = "token_only"
)

// MediaModality identifies one media modality.
type MediaModality string

const (
	MediaModalityImage    MediaModality = "image"
	MediaModalityAudio    MediaModality = "audio"
	MediaModalityVideo    MediaModality = "video"
	MediaModalityDocument MediaModality = "document"
	MediaModalityFile     MediaModality = "file"
)

// MediaSourceKind identifies how one media item is referenced.
type MediaSourceKind string

const (
	MediaSourceInline       MediaSourceKind = "inline"
	MediaSourceURL          MediaSourceKind = "url"
	MediaSourceProviderFile MediaSourceKind = "provider_file"
	MediaSourceLocalRef     MediaSourceKind = "local_ref"
)

// ToolSpecKind identifies one tool declaration family.
type ToolSpecKind string

const (
	ToolSpecKindFunction         ToolSpecKind = "function"
	ToolSpecKindProviderDefined  ToolSpecKind = "provider_defined"
	ToolSpecKindProviderExecuted ToolSpecKind = "provider_executed"
	ToolSpecKindMCP              ToolSpecKind = "mcp"
)

// OutputMode identifies one desired model output contract.
type OutputMode string

const (
	OutputModeText     OutputMode = "text"
	OutputModeJSON     OutputMode = "json"
	OutputModeSchema   OutputMode = "schema"
	OutputModeToolOnly OutputMode = "tool_only"
)

// ResponseStatus identifies one model response lifecycle state.
type ResponseStatus string

const (
	ResponseStatusInProgress ResponseStatus = "in_progress"
	ResponseStatusCompleted  ResponseStatus = "completed"
	ResponseStatusCancelled  ResponseStatus = "cancelled"
	ResponseStatusFailed     ResponseStatus = "failed"
)

// StreamEventType identifies one provider stream event shape.
type StreamEventType string

const (
	StreamEventPartStart   StreamEventType = "part_start"
	StreamEventPartDelta   StreamEventType = "part_delta"
	StreamEventPartDone    StreamEventType = "part_done"
	StreamEventMessageDone StreamEventType = "message_done"
	StreamEventStepDone    StreamEventType = "step_done"
	StreamEventTurnDone    StreamEventType = "turn_done"
	StreamEventRawProvider StreamEventType = "raw_provider_event"
)

// ReplayMeta preserves replay metadata such as reasoning signatures.
type ReplayMeta struct {
	Provider string `json:"provider,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Token    string `json:"token,omitempty"`
}

// MessageOrigin records provider/model metadata about one message.
type MessageOrigin struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	RawFinishReason string `json:"raw_finish_reason,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
}

type TextPart struct {
	Text string `json:"text"`
}

type ReasoningPart struct {
	VisibleText     *string                    `json:"visible_text,omitempty"`
	Visibility      ReasoningVisibility        `json:"visibility,omitempty"`
	Replay          *ReplayMeta                `json:"replay,omitempty"`
	ProviderDetails map[string]json.RawMessage `json:"provider_details,omitempty"`
}

type ToolUsePart struct {
	ID              string                     `json:"id,omitempty"`
	Name            string                     `json:"name,omitempty"`
	Input           json.RawMessage            `json:"input,omitempty"`
	Replay          *ReplayMeta                `json:"replay,omitempty"`
	ProviderDetails map[string]json.RawMessage `json:"provider_details,omitempty"`
}

type ToolResultPart struct {
	ToolUseID string `json:"tool_use_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Content   []Part `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type MediaSource struct {
	Kind     MediaSourceKind `json:"kind,omitempty"`
	Data     string          `json:"data,omitempty"`
	URI      string          `json:"uri,omitempty"`
	FileID   string          `json:"file_id,omitempty"`
	LocalRef string          `json:"local_ref,omitempty"`
}

type MediaPart struct {
	Modality MediaModality `json:"modality,omitempty"`
	Source   MediaSource   `json:"source"`
	MimeType string        `json:"mime_type,omitempty"`
	Name     string        `json:"name,omitempty"`
}

type JSONPart struct {
	Value json.RawMessage `json:"value,omitempty"`
}

type FileRefPart struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	URI      string `json:"uri,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	LocalRef string `json:"local_ref,omitempty"`
}

// Part is the provider-neutral semantic message unit.
type Part struct {
	Kind       PartKind        `json:"kind"`
	Text       *TextPart       `json:"text,omitempty"`
	Reasoning  *ReasoningPart  `json:"reasoning,omitempty"`
	ToolUse    *ToolUsePart    `json:"tool_use,omitempty"`
	ToolResult *ToolResultPart `json:"tool_result,omitempty"`
	Media      *MediaPart      `json:"media,omitempty"`
	JSON       *JSONPart       `json:"json,omitempty"`
	FileRef    *FileRefPart    `json:"file_ref,omitempty"`
}

func NewTextPart(text string) Part {
	return Part{Kind: PartKindText, Text: &TextPart{Text: text}}
}

func NewReasoningPart(text string, visibility ReasoningVisibility) Part {
	visible := text
	part := &ReasoningPart{Visibility: visibility}
	if part.Visibility == "" {
		part.Visibility = ReasoningVisibilityVisible
	}
	if text != "" {
		part.VisibleText = &visible
	}
	return Part{Kind: PartKindReasoning, Reasoning: part}
}

func NewToolUsePart(id, name string, input json.RawMessage) Part {
	return Part{
		Kind: PartKindToolUse,
		ToolUse: &ToolUsePart{
			ID:    id,
			Name:  name,
			Input: normalizeToolUseInput(input),
		},
	}
}

const (
	rawToolUseInputKey        = "__caelis_raw_tool_input"
	rawToolUseInputWrappedKey = "__caelis_raw_tool_input_wrapped"
)

func normalizeToolUseInput(input json.RawMessage) json.RawMessage {
	raw := strings.TrimSpace(string(input))
	if raw == "" {
		return nil
	}
	if json.Valid([]byte(raw)) {
		return append(json.RawMessage(nil), raw...)
	}
	wrapped, err := json.Marshal(map[string]any{
		rawToolUseInputWrappedKey: true,
		rawToolUseInputKey:        raw,
	})
	if err != nil {
		return nil
	}
	return json.RawMessage(wrapped)
}

func NewToolResultJSONPart(toolUseID, name string, value map[string]any, isError bool) Part {
	raw, _ := json.Marshal(value)
	return Part{
		Kind: PartKindToolResult,
		ToolResult: &ToolResultPart{
			ToolUseID: toolUseID,
			Name:      name,
			Content:   []Part{{Kind: PartKindJSON, JSON: &JSONPart{Value: raw}}},
			IsError:   isError,
		},
	}
}

func NewMediaPart(modality MediaModality, source MediaSource, mimeType, name string) Part {
	return Part{
		Kind: PartKindMedia,
		Media: &MediaPart{
			Modality: modality,
			Source:   source,
			MimeType: mimeType,
			Name:     name,
		},
	}
}

func NewJSONPart(raw json.RawMessage) Part {
	return Part{Kind: PartKindJSON, JSON: &JSONPart{Value: append(json.RawMessage(nil), raw...)}}
}

func NewFileRefPart(name, mimeType, uri, fileID, localRef string) Part {
	return Part{
		Kind:    PartKindFileRef,
		FileRef: &FileRefPart{Name: name, MimeType: mimeType, URI: uri, FileID: fileID, LocalRef: localRef},
	}
}

func NewMessage(role Role, parts ...Part) Message {
	return Message{Role: role, Parts: CloneParts(parts)}
}

func NewTextMessage(role Role, text string) Message {
	if text == "" {
		return Message{Role: role}
	}
	return NewMessage(role, NewTextPart(text))
}

func NewReasoningMessage(role Role, text string, visibility ReasoningVisibility) Message {
	if text == "" {
		return Message{Role: role}
	}
	return NewMessage(role, NewReasoningPart(text, visibility))
}

func PartFromContentPart(part ContentPart) Part {
	switch part.Type {
	case ContentPartImage:
		return NewMediaPart(MediaModalityImage, MediaSource{
			Kind: MediaSourceInline,
			Data: part.Data,
		}, part.MimeType, part.FileName)
	default:
		return NewTextPart(part.Text)
	}
}

func PartsFromContentParts(parts []ContentPart) []Part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]Part, 0, len(parts))
	for _, part := range parts {
		out = append(out, PartFromContentPart(part))
	}
	return out
}

func ContentPartFromPart(part Part) (ContentPart, bool) {
	switch part.Kind {
	case PartKindText:
		if part.Text == nil {
			return ContentPart{}, false
		}
		return ContentPart{Type: ContentPartText, Text: part.Text.Text}, true
	case PartKindMedia:
		if part.Media == nil || part.Media.Modality != MediaModalityImage {
			return ContentPart{}, false
		}
		if part.Media.Source.Kind != MediaSourceInline {
			return ContentPart{}, false
		}
		return ContentPart{
			Type:     ContentPartImage,
			MimeType: part.Media.MimeType,
			Data:     part.Media.Source.Data,
			FileName: part.Media.Name,
		}, true
	default:
		return ContentPart{}, false
	}
}

func ContentPartsFromParts(parts []Part) []ContentPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]ContentPart, 0, len(parts))
	for _, part := range parts {
		cp, ok := ContentPartFromPart(part)
		if ok {
			out = append(out, cp)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MessageFromContentParts converts prompt content parts into one message.
func MessageFromContentParts(role Role, parts []ContentPart) Message {
	return Message{Role: role, Parts: PartsFromContentParts(parts)}
}

// MessageFromTextAndContentParts merges one plain-text prompt with one content
// part slice into one provider-neutral message.
func MessageFromTextAndContentParts(role Role, text string, parts []ContentPart) Message {
	prepared := append([]ContentPart(nil), parts...)
	if strings.TrimSpace(text) != "" && len(prepared) == 0 {
		return NewTextMessage(role, text)
	}
	if strings.TrimSpace(text) != "" {
		hasText := false
		for _, part := range prepared {
			if part.Type == ContentPartText && strings.TrimSpace(part.Text) != "" {
				hasText = true
				break
			}
		}
		if !hasText {
			prepared = append([]ContentPart{{Type: ContentPartText, Text: text}}, prepared...)
		}
	}
	return MessageFromContentParts(role, prepared)
}

func MessageFromToolCalls(role Role, calls []ToolCall, text string) Message {
	parts := make([]Part, 0, len(calls)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, NewTextPart(text))
	}
	for _, call := range calls {
		part := NewToolUsePart(call.ID, call.Name, json.RawMessage(strings.TrimSpace(call.Args)))
		if part.ToolUse != nil && strings.TrimSpace(call.ThoughtSignature) != "" {
			part.ToolUse.Replay = &ReplayMeta{Token: call.ThoughtSignature}
		}
		parts = append(parts, part)
	}
	return Message{Role: role, Parts: parts}
}

func MessageFromAssistantParts(text string, reasoning string, calls []ToolCall) Message {
	parts := make([]Part, 0, len(calls)+2)
	if strings.TrimSpace(reasoning) != "" {
		parts = append(parts, NewReasoningPart(reasoning, ReasoningVisibilityVisible))
	}
	if strings.TrimSpace(text) != "" {
		parts = append(parts, NewTextPart(text))
	}
	for _, call := range calls {
		part := NewToolUsePart(call.ID, call.Name, json.RawMessage(strings.TrimSpace(call.Args)))
		if part.ToolUse != nil && strings.TrimSpace(call.ThoughtSignature) != "" {
			part.ToolUse.Replay = &ReplayMeta{Token: call.ThoughtSignature}
		}
		parts = append(parts, part)
	}
	return Message{Role: RoleAssistant, Parts: parts}
}

func MessageFromToolResponse(resp *ToolResponse) Message {
	if resp == nil {
		return Message{Role: RoleTool}
	}
	return Message{
		Role:  RoleTool,
		Parts: []Part{NewToolResultJSONPart(resp.ID, resp.Name, resp.Result, false)},
	}
}

type FunctionToolSpec struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type ProviderDefinedToolSpec struct {
	Name            string                     `json:"name,omitempty"`
	Provider        string                     `json:"provider,omitempty"`
	ProviderDetails map[string]json.RawMessage `json:"provider_details,omitempty"`
}

type ProviderExecutedToolSpec struct {
	Name            string                     `json:"name,omitempty"`
	Provider        string                     `json:"provider,omitempty"`
	ProviderDetails map[string]json.RawMessage `json:"provider_details,omitempty"`
}

type MCPToolSpec struct {
	Name   string `json:"name,omitempty"`
	Server string `json:"server,omitempty"`
	Tool   string `json:"tool,omitempty"`
}

// ToolSpec is the provider-neutral visible tool declaration.
type ToolSpec struct {
	Kind             ToolSpecKind              `json:"kind"`
	Function         *FunctionToolSpec         `json:"function,omitempty"`
	ProviderDefined  *ProviderDefinedToolSpec  `json:"provider_defined,omitempty"`
	ProviderExecuted *ProviderExecutedToolSpec `json:"provider_executed,omitempty"`
	MCP              *MCPToolSpec              `json:"mcp,omitempty"`
}

func NewFunctionToolSpec(name, description string, parameters map[string]any) ToolSpec {
	return ToolSpec{
		Kind: ToolSpecKindFunction,
		Function: &FunctionToolSpec{
			Name:        name,
			Description: description,
			Parameters:  cloneAnyMap(parameters),
		},
	}
}

// ToolDefinition remains the concrete function-tool declaration used by simple registries.
type ToolDefinition = FunctionToolSpec

func ToolSpecsFromDefinitions(defs []ToolDefinition) []ToolSpec {
	if len(defs) == 0 {
		return nil
	}
	out := make([]ToolSpec, 0, len(defs))
	for _, def := range defs {
		out = append(out, NewFunctionToolSpec(def.Name, def.Description, def.Parameters))
	}
	return out
}

func FunctionToolDefinitions(specs []ToolSpec) []ToolDefinition {
	if len(specs) == 0 {
		return nil
	}
	out := make([]ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		if spec.Kind != ToolSpecKindFunction || spec.Function == nil {
			continue
		}
		out = append(out, ToolDefinition{
			Name:        spec.Function.Name,
			Description: spec.Function.Description,
			Parameters:  cloneAnyMap(spec.Function.Parameters),
		})
	}
	return out
}

// OutputSpec defines the desired output contract for one request.
type OutputSpec struct {
	Mode            OutputMode     `json:"mode,omitempty"`
	JSONSchema      map[string]any `json:"json_schema,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
}

// Message is one semantic message in model context.
type Message struct {
	Role   Role           `json:"role"`
	Parts  []Part         `json:"parts,omitempty"`
	Origin *MessageOrigin `json:"origin,omitempty"`
}

func (m Message) TextContent() string {
	texts := make([]string, 0, len(m.Parts))
	for _, part := range m.Parts {
		if part.Text != nil && part.Text.Text != "" {
			texts = append(texts, part.Text.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func (m Message) ReasoningParts() []ReasoningPart {
	out := make([]ReasoningPart, 0, len(m.Parts))
	for _, part := range m.Parts {
		if part.Reasoning != nil {
			out = append(out, *cloneReasoningPart(part.Reasoning))
		}
	}
	return out
}

func (m Message) ToolUses() []ToolUsePart {
	out := make([]ToolUsePart, 0, len(m.Parts))
	for _, part := range m.Parts {
		if part.ToolUse != nil {
			out = append(out, *cloneToolUsePart(part.ToolUse))
		}
	}
	return out
}

func (m Message) ToolCalls() []ToolCall {
	uses := m.ToolUses()
	out := make([]ToolCall, 0, len(uses))
	for _, use := range uses {
		out = append(out, ToolCall{
			ID:               use.ID,
			Name:             use.Name,
			Args:             toolUseInputArgs(use.Input),
			ThoughtSignature: replayToken(use.Replay),
		})
	}
	return out
}

func toolUseInputArgs(input json.RawMessage) string {
	raw := strings.TrimSpace(string(input))
	if raw == "" {
		return ""
	}
	var wrapped map[string]any
	if err := json.Unmarshal(input, &wrapped); err == nil && len(wrapped) == 2 {
		if wrapped[rawToolUseInputWrappedKey] == true {
			if value, ok := wrapped[rawToolUseInputKey].(string); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	return raw
}

func (m Message) ToolResults() []ToolResultPart {
	out := make([]ToolResultPart, 0, len(m.Parts))
	for _, part := range m.Parts {
		if part.ToolResult != nil {
			out = append(out, *cloneToolResultPart(part.ToolResult))
		}
	}
	return out
}

func (m Message) ToolResponse() *ToolResponse {
	results := m.ToolResults()
	if len(results) == 0 {
		return nil
	}
	first := results[0]
	out := &ToolResponse{
		ID:   first.ToolUseID,
		Name: first.Name,
	}
	if len(first.Content) > 0 {
		if raw := first.Content[0].JSONValue(); len(raw) > 0 {
			_ = json.Unmarshal(raw, &out.Result)
		}
	}
	if out.Result == nil {
		out.Result = map[string]any{}
	}
	return out
}

func (m Message) HasMedia(modality MediaModality) bool {
	for _, part := range m.Parts {
		if part.Media == nil {
			continue
		}
		if modality == "" || part.Media.Modality == modality {
			return true
		}
	}
	return false
}

func (m Message) HasImages() bool {
	return m.HasMedia(MediaModalityImage)
}

func (m Message) ReasoningText() string {
	parts := m.ReasoningParts()
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.VisibleText != nil && *part.VisibleText != "" {
			texts = append(texts, *part.VisibleText)
		}
	}
	return strings.Join(texts, "\n")
}

// ReasoningConfig controls provider reasoning/thinking behavior.
type ReasoningConfig struct {
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Effort       string `json:"effort,omitempty"`
}

// Request is the provider-agnostic model request contract.
type Request struct {
	Instructions []Part          `json:"instructions,omitempty"`
	Messages     []Message       `json:"messages,omitempty"`
	Tools        []ToolSpec      `json:"tools,omitempty"`
	Output       *OutputSpec     `json:"output,omitempty"`
	Reasoning    ReasoningConfig `json:"reasoning,omitempty"`
	Stream       bool            `json:"stream,omitempty"`
}

// Usage reports model token usage on a best-effort basis.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

// FinishReason describes why a model turn ended.
type FinishReason string

const (
	FinishReasonUnknown       FinishReason = ""
	FinishReasonStop          FinishReason = "stop"
	FinishReasonLength        FinishReason = "length"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonContentFilter FinishReason = "content_filter"
)

// PartDelta is one incremental provider-neutral part update.
type PartDelta struct {
	Index           int                        `json:"index,omitempty"`
	Kind            PartKind                   `json:"kind,omitempty"`
	TextDelta       string                     `json:"text_delta,omitempty"`
	InputDelta      string                     `json:"input_delta,omitempty"`
	Replay          *ReplayMeta                `json:"replay,omitempty"`
	ProviderDetails map[string]json.RawMessage `json:"provider_details,omitempty"`
}

// Response is the completed semantic result of one step or turn.
type Response struct {
	Message         Message        `json:"message"`
	StepComplete    bool           `json:"step_complete,omitempty"`
	TurnComplete    bool           `json:"turn_complete,omitempty"`
	Status          ResponseStatus `json:"status,omitempty"`
	FinishReason    FinishReason   `json:"finish_reason,omitempty"`
	RawFinishReason string         `json:"raw_finish_reason,omitempty"`
	Usage           Usage          `json:"usage,omitempty"`
	Model           string         `json:"model,omitempty"`
	Provider        string         `json:"provider,omitempty"`
}

// StreamEvent is the unified event contract consumed by upper layers.
type StreamEvent struct {
	Type      StreamEventType `json:"type"`
	PartDelta *PartDelta      `json:"part_delta,omitempty"`
	Message   *Message        `json:"message,omitempty"`
	*Response
	RawProviderEvent json.RawMessage `json:"raw_provider_event,omitempty"`
}

func StreamEventFromResponse(resp *Response) *StreamEvent {
	if resp == nil {
		return nil
	}
	cp := *resp
	cp.Message = CloneMessage(resp.Message)
	eventType := StreamEventMessageDone
	switch {
	case cp.TurnComplete:
		eventType = StreamEventTurnDone
	case cp.StepComplete:
		eventType = StreamEventStepDone
	}
	return &StreamEvent{
		Type:     eventType,
		Message:  &cp.Message,
		Response: &cp,
	}
}

// LLM is the provider-neutral model boundary used by the future runtime.
type LLM interface {
	Name() string
	Generate(context.Context, *Request) iter.Seq2[*StreamEvent, error]
}

// ContextOverflowError indicates the request exceeds the model context window.
type ContextOverflowError struct {
	Cause error
}

func (e *ContextOverflowError) Error() string {
	if e.Cause != nil {
		return "model: context overflow: " + e.Cause.Error()
	}
	return "model: context overflow"
}

func (e *ContextOverflowError) Unwrap() error { return e.Cause }

func IsContextOverflow(err error) bool {
	var coe *ContextOverflowError
	return errors.As(err, &coe)
}

func CloneMessage(msg Message) Message {
	cp := msg
	cp.Parts = CloneParts(msg.Parts)
	if msg.Origin != nil {
		origin := *msg.Origin
		cp.Origin = &origin
	}
	return cp
}

func CloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		out = append(out, CloneMessage(msg))
	}
	return out
}

func CloneParts(parts []Part) []Part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]Part, 0, len(parts))
	for _, part := range parts {
		cp := part
		if part.Text != nil {
			text := *part.Text
			cp.Text = &text
		}
		cp.Reasoning = cloneReasoningPart(part.Reasoning)
		cp.ToolUse = cloneToolUsePart(part.ToolUse)
		cp.ToolResult = cloneToolResultPart(part.ToolResult)
		if part.Media != nil {
			media := *part.Media
			cp.Media = &media
		}
		if part.JSON != nil {
			cp.JSON = &JSONPart{Value: append(json.RawMessage(nil), part.JSON.Value...)}
		}
		if part.FileRef != nil {
			fileRef := *part.FileRef
			cp.FileRef = &fileRef
		}
		out = append(out, cp)
	}
	return out
}

func (p Part) JSONValue() json.RawMessage {
	if p.JSON == nil {
		return nil
	}
	return append(json.RawMessage(nil), p.JSON.Value...)
}

func cloneReasoningPart(part *ReasoningPart) *ReasoningPart {
	if part == nil {
		return nil
	}
	cp := *part
	if part.VisibleText != nil {
		text := *part.VisibleText
		cp.VisibleText = &text
	}
	if part.Replay != nil {
		replay := *part.Replay
		cp.Replay = &replay
	}
	if len(part.ProviderDetails) > 0 {
		cp.ProviderDetails = make(map[string]json.RawMessage, len(part.ProviderDetails))
		for key, value := range part.ProviderDetails {
			cp.ProviderDetails[key] = append(json.RawMessage(nil), value...)
		}
	}
	return &cp
}

func cloneToolUsePart(part *ToolUsePart) *ToolUsePart {
	if part == nil {
		return nil
	}
	cp := *part
	cp.Input = append(json.RawMessage(nil), part.Input...)
	if part.Replay != nil {
		replay := *part.Replay
		cp.Replay = &replay
	}
	if len(part.ProviderDetails) > 0 {
		cp.ProviderDetails = make(map[string]json.RawMessage, len(part.ProviderDetails))
		for key, value := range part.ProviderDetails {
			cp.ProviderDetails[key] = append(json.RawMessage(nil), value...)
		}
	}
	return &cp
}

func cloneToolResultPart(part *ToolResultPart) *ToolResultPart {
	if part == nil {
		return nil
	}
	cp := *part
	cp.Content = CloneParts(part.Content)
	return &cp
}

func replayToken(meta *ReplayMeta) string {
	if meta == nil {
		return ""
	}
	return strings.TrimSpace(meta.Token)
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
