// Package model defines provider-neutral model messages, requests, and
// provider contracts for the reimplemented Caelis core.
package model

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
)

// Role identifies the semantic author of one model message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentPartType identifies one user-facing prompt input unit.
type ContentPartType string

const (
	ContentPartText  ContentPartType = "text"
	ContentPartImage ContentPartType = "image"
	ContentPartFile  ContentPartType = "file"
)

// ContentPart is prompt input before it has been normalized into a model
// message.
type ContentPart struct {
	Type     ContentPartType `json:"type"`
	Text     string          `json:"text,omitempty"`
	MimeType string          `json:"mime_type,omitempty"`
	Data     string          `json:"data,omitempty"`
	URI      string          `json:"uri,omitempty"`
	FileName string          `json:"file_name,omitempty"`
}

// PartKind identifies one provider-neutral message part.
type PartKind string

const (
	PartText       PartKind = "text"
	PartReasoning  PartKind = "reasoning"
	PartToolUse    PartKind = "tool_use"
	PartToolResult PartKind = "tool_result"
	PartMedia      PartKind = "media"
	PartJSON       PartKind = "json"
	PartFileRef    PartKind = "file_ref"
)

// ReasoningVisibility describes whether reasoning text may be shown to
// surfaces while remaining replayable for providers that require signatures.
type ReasoningVisibility string

const (
	ReasoningVisible   ReasoningVisibility = "visible"
	ReasoningHidden    ReasoningVisibility = "hidden"
	ReasoningRedacted  ReasoningVisibility = "redacted"
	ReasoningTokenOnly ReasoningVisibility = "token_only"
)

// ReplayMeta preserves provider replay metadata such as reasoning signatures.
type ReplayMeta struct {
	Provider string `json:"provider,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Token    string `json:"token,omitempty"`
}

// Origin records provider metadata for one message.
type Origin struct {
	Provider        string                     `json:"provider,omitempty"`
	Model           string                     `json:"model,omitempty"`
	RawFinishReason string                     `json:"raw_finish_reason,omitempty"`
	CreatedAt       time.Time                  `json:"created_at,omitempty"`
	Metadata        map[string]json.RawMessage `json:"metadata,omitempty"`
}

type TextPart struct {
	Text string `json:"text"`
}

type ReasoningPart struct {
	VisibleText     string                     `json:"visible_text,omitempty"`
	Visibility      ReasoningVisibility        `json:"visibility,omitempty"`
	Replay          *ReplayMeta                `json:"replay,omitempty"`
	ProviderDetails map[string]json.RawMessage `json:"provider_details,omitempty"`
}

// ToolCall is one provider-neutral model-emitted tool invocation.
type ToolCall struct {
	ID              string                     `json:"id,omitempty"`
	Name            string                     `json:"name,omitempty"`
	Input           json.RawMessage            `json:"input,omitempty"`
	Replay          *ReplayMeta                `json:"replay,omitempty"`
	ProviderDetails map[string]json.RawMessage `json:"provider_details,omitempty"`
}

// ToolResultPart is the model-visible response to a tool call.
type ToolResultPart struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Content    []Part `json:"content,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

type MediaModality string

const (
	MediaImage    MediaModality = "image"
	MediaAudio    MediaModality = "audio"
	MediaVideo    MediaModality = "video"
	MediaDocument MediaModality = "document"
	MediaFile     MediaModality = "file"
)

type MediaSourceKind string

const (
	MediaInline       MediaSourceKind = "inline"
	MediaURL          MediaSourceKind = "url"
	MediaProviderFile MediaSourceKind = "provider_file"
	MediaLocalRef     MediaSourceKind = "local_ref"
)

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

// Part is one provider-neutral semantic message part.
type Part struct {
	Kind       PartKind        `json:"kind"`
	Text       *TextPart       `json:"text,omitempty"`
	Reasoning  *ReasoningPart  `json:"reasoning,omitempty"`
	ToolUse    *ToolCall       `json:"tool_use,omitempty"`
	ToolResult *ToolResultPart `json:"tool_result,omitempty"`
	Media      *MediaPart      `json:"media,omitempty"`
	JSON       *JSONPart       `json:"json,omitempty"`
	FileRef    *FileRefPart    `json:"file_ref,omitempty"`
}

// Message is the durable model-visible unit. Runtime replay is derived from
// this type, not from UI transcript or protocol projection data.
type Message struct {
	ID     string         `json:"id,omitempty"`
	Role   Role           `json:"role"`
	Parts  []Part         `json:"parts,omitempty"`
	Origin *Origin        `json:"origin,omitempty"`
	Usage  *Usage         `json:"usage,omitempty"`
	Meta   map[string]any `json:"meta,omitempty"`
}

// Usage is the provider-neutral token accounting shape used by runtime,
// session events, and surfaces.
type Usage struct {
	InputTokens         int `json:"input_tokens,omitempty"`
	CachedInputTokens   int `json:"cached_input_tokens,omitempty"`
	OutputTokens        int `json:"output_tokens,omitempty"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
	TotalTokens         int `json:"total_tokens,omitempty"`
	ContextWindowTokens int `json:"context_window_tokens,omitempty"`
}

// APIType identifies the model-provider protocol dialect for one configured
// endpoint. It belongs to core because model provider selection and settings
// are shared by CLI, TUI, ACP, and future APP surfaces.
type APIType string

const (
	APIOpenAI              APIType = "openai"
	APIOpenAICompatible    APIType = "openai_compatible"
	APIOpenRouter          APIType = "openrouter"
	APICodeFree            APIType = "codefree"
	APIGemini              APIType = "gemini"
	APIAnthropic           APIType = "anthropic"
	APIAnthropicCompatible APIType = "anthropic_compatible"
	APIDeepSeek            APIType = "deepseek"
	APIMiniMax             APIType = "minimax"
	APIVolcengine          APIType = "volcengine"
	APIMimo                APIType = "mimo"
	APIVolcengineCoding    APIType = "volcengine_coding_plan"
	APIOllama              APIType = "ollama"
)

// AuthType identifies how a model-provider endpoint authenticates.
type AuthType string

const (
	AuthAPIKey      AuthType = "api_key"
	AuthBearerToken AuthType = "bearer_token"
	AuthOAuthToken  AuthType = "oauth_token"
	AuthNone        AuthType = "none"
)

// ToolSpecKind identifies one model tool declaration family.
type ToolSpecKind string

const (
	ToolSpecFunction         ToolSpecKind = "function"
	ToolSpecProviderDefined  ToolSpecKind = "provider_defined"
	ToolSpecProviderExecuted ToolSpecKind = "provider_executed"
	ToolSpecMCP              ToolSpecKind = "mcp"
)

// ToolSpec is the model-facing declaration produced by tool registries.
type ToolSpec struct {
	Kind             ToolSpecKind               `json:"kind,omitempty"`
	Name             string                     `json:"name,omitempty"`
	Description      string                     `json:"description,omitempty"`
	InputSchema      map[string]any             `json:"input_schema,omitempty"`
	ProviderPayloads map[string]json.RawMessage `json:"provider_payloads,omitempty"`
	Meta             map[string]any             `json:"meta,omitempty"`
}

type OutputMode string

const (
	OutputText     OutputMode = "text"
	OutputJSON     OutputMode = "json"
	OutputSchema   OutputMode = "schema"
	OutputToolOnly OutputMode = "tool_only"
)

type OutputSpec struct {
	Mode       OutputMode     `json:"mode,omitempty"`
	JSONSchema map[string]any `json:"json_schema,omitempty"`
}

type ReasoningConfig struct {
	Effort     string              `json:"effort,omitempty"`
	Budget     int                 `json:"budget,omitempty"`
	Visibility ReasoningVisibility `json:"visibility,omitempty"`
}

// Request is one provider-neutral model invocation.
type Request struct {
	Model        string          `json:"model,omitempty"`
	Messages     []Message       `json:"messages,omitempty"`
	Tools        []ToolSpec      `json:"tools,omitempty"`
	Instructions []string        `json:"instructions,omitempty"`
	Reasoning    ReasoningConfig `json:"reasoning,omitempty"`
	Output       *OutputSpec     `json:"output,omitempty"`
	Stream       bool            `json:"stream,omitempty"`
	Meta         map[string]any  `json:"meta,omitempty"`
}

type ResponseStatus string

const (
	ResponseInProgress ResponseStatus = "in_progress"
	ResponseCompleted  ResponseStatus = "completed"
	ResponseCancelled  ResponseStatus = "cancelled"
	ResponseFailed     ResponseStatus = "failed"
)

// Response is the final provider-neutral result for one model request.
type Response struct {
	Message Message        `json:"message"`
	Status  ResponseStatus `json:"status,omitempty"`
	Error   string         `json:"error,omitempty"`
	Usage   *Usage         `json:"usage,omitempty"`
	Origin  *Origin        `json:"origin,omitempty"`
}

type StreamEventType string

const (
	StreamPartStart   StreamEventType = "part_start"
	StreamPartDelta   StreamEventType = "part_delta"
	StreamPartDone    StreamEventType = "part_done"
	StreamMessageDone StreamEventType = "message_done"
	StreamTurnDone    StreamEventType = "turn_done"
	StreamRawProvider StreamEventType = "raw_provider_event"
)

// StreamEvent is the provider-neutral streaming frame.
type StreamEvent struct {
	Type     StreamEventType `json:"type"`
	Message  *Message        `json:"message,omitempty"`
	Part     *Part           `json:"part,omitempty"`
	Delta    string          `json:"delta,omitempty"`
	Response *Response       `json:"response,omitempty"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

// Stream is a pull-based model stream. io.EOF marks normal completion.
type Stream interface {
	Recv() (StreamEvent, error)
	Close() error
}

// ModelInfo describes one provider-visible model option.
type ModelInfo struct {
	ID                     string   `json:"id,omitempty"`
	Name                   string   `json:"name,omitempty"`
	Provider               string   `json:"provider,omitempty"`
	ContextWindowTokens    int      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens        int      `json:"max_output_tokens,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	SupportsToolCalls      bool     `json:"supports_tool_calls,omitempty"`
	SupportsImages         bool     `json:"supports_images,omitempty"`
	SupportsJSON           bool     `json:"supports_json,omitempty"`
}

// Provider is the stable model-provider boundary consumed by the runtime.
type Provider interface {
	ID() string
	Models(context.Context) ([]ModelInfo, error)
	Stream(context.Context, Request) (Stream, error)
}

// StaticStream is a simple in-memory Stream useful for tests and adapters that
// already buffered a response.
type StaticStream struct {
	Events []StreamEvent
	index  int
	closed bool
}

func (s *StaticStream) Recv() (StreamEvent, error) {
	if s == nil || s.closed || s.index >= len(s.Events) {
		return StreamEvent{}, io.EOF
	}
	event := s.Events[s.index]
	s.index++
	return event, nil
}

func (s *StaticStream) Close() error {
	if s != nil {
		s.closed = true
	}
	return nil
}

func NewTextPart(text string) Part {
	return Part{Kind: PartText, Text: &TextPart{Text: text}}
}

func NewReasoningPart(text string, visibility ReasoningVisibility) Part {
	if visibility == "" {
		visibility = ReasoningVisible
	}
	return Part{Kind: PartReasoning, Reasoning: &ReasoningPart{
		VisibleText: text,
		Visibility:  visibility,
	}}
}

func NewFunctionToolSpec(name string, description string, inputSchema map[string]any) ToolSpec {
	return ToolSpec{
		Kind:        ToolSpecFunction,
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		InputSchema: maps.Clone(inputSchema),
	}
}

func NewProviderDefinedToolSpec(name string, payloads map[string]json.RawMessage) ToolSpec {
	return NewProviderToolSpec(ToolSpecProviderDefined, name, payloads)
}

func NewProviderExecutedToolSpec(name string, payloads map[string]json.RawMessage) ToolSpec {
	return NewProviderToolSpec(ToolSpecProviderExecuted, name, payloads)
}

func NewMCPToolSpec(name string, payloads map[string]json.RawMessage) ToolSpec {
	return NewProviderToolSpec(ToolSpecMCP, name, payloads)
}

func NewProviderToolSpec(kind ToolSpecKind, name string, payloads map[string]json.RawMessage) ToolSpec {
	if kind == "" {
		kind = ToolSpecProviderDefined
	}
	return ToolSpec{
		Kind:             kind,
		Name:             strings.TrimSpace(name),
		ProviderPayloads: cloneProviderPayloads(payloads),
	}
}

func ProviderToolPayload(spec ToolSpec, providers ...string) (json.RawMessage, bool) {
	if len(spec.ProviderPayloads) == 0 {
		return nil, false
	}
	for _, provider := range providers {
		if raw, ok := providerPayload(spec.ProviderPayloads, provider); ok {
			return raw, true
		}
	}
	for _, provider := range []string{"default", "*"} {
		if raw, ok := providerPayload(spec.ProviderPayloads, provider); ok {
			return raw, true
		}
	}
	return nil, false
}

func CloneToolSpec(in ToolSpec) ToolSpec {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Description = strings.TrimSpace(in.Description)
	out.InputSchema = maps.Clone(in.InputSchema)
	out.ProviderPayloads = cloneProviderPayloads(in.ProviderPayloads)
	out.Meta = maps.Clone(in.Meta)
	return out
}

func CloneToolSpecs(in []ToolSpec) []ToolSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolSpec, 0, len(in))
	for _, spec := range in {
		out = append(out, CloneToolSpec(spec))
	}
	return out
}

func providerPayload(payloads map[string]json.RawMessage, provider string) (json.RawMessage, bool) {
	key := normalizeProviderPayloadKey(provider)
	if key == "" {
		return nil, false
	}
	raw, ok := payloads[key]
	if !ok || len(raw) == 0 {
		return nil, false
	}
	return slices.Clone(raw), true
}

func cloneProviderPayloads(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for provider, raw := range in {
		key := normalizeProviderPayloadKey(provider)
		if key == "" || len(raw) == 0 {
			continue
		}
		out[key] = slices.Clone(raw)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeProviderPayloadKey(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func (m Message) TextContent() string {
	var parts []string
	for _, part := range m.Parts {
		if part.Kind == PartText && part.Text != nil {
			if text := strings.TrimSpace(part.Text.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func (m Message) ToolCalls() []ToolCall {
	var out []ToolCall
	for _, part := range m.Parts {
		if part.Kind == PartToolUse && part.ToolUse != nil {
			out = append(out, CloneToolCall(*part.ToolUse))
		}
	}
	return out
}

func CloneMessage(in Message) Message {
	out := in
	out.ID = strings.TrimSpace(in.ID)
	out.Parts = cloneParts(in.Parts)
	out.Meta = maps.Clone(in.Meta)
	if in.Origin != nil {
		origin := *in.Origin
		origin.Metadata = maps.Clone(in.Origin.Metadata)
		out.Origin = &origin
	}
	if in.Usage != nil {
		usage := *in.Usage
		out.Usage = &usage
	}
	return out
}

func CloneContentParts(in []ContentPart) []ContentPart {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	for i := range out {
		out[i].Text = strings.TrimSpace(out[i].Text)
		out[i].MimeType = strings.TrimSpace(out[i].MimeType)
		out[i].Data = strings.TrimSpace(out[i].Data)
		out[i].URI = strings.TrimSpace(out[i].URI)
		out[i].FileName = strings.TrimSpace(out[i].FileName)
	}
	return out
}

func CloneToolCall(in ToolCall) ToolCall {
	out := in
	out.ID = strings.TrimSpace(in.ID)
	out.Name = strings.TrimSpace(in.Name)
	out.Input = slices.Clone(in.Input)
	out.ProviderDetails = maps.Clone(in.ProviderDetails)
	if in.Replay != nil {
		replay := *in.Replay
		out.Replay = &replay
	}
	return out
}

func CloneParts(in []Part) []Part {
	return cloneParts(in)
}

func cloneParts(in []Part) []Part {
	if len(in) == 0 {
		return nil
	}
	out := make([]Part, 0, len(in))
	for _, part := range in {
		next := part
		if part.Text != nil {
			text := *part.Text
			next.Text = &text
		}
		if part.Reasoning != nil {
			reasoning := *part.Reasoning
			reasoning.ProviderDetails = maps.Clone(part.Reasoning.ProviderDetails)
			if part.Reasoning.Replay != nil {
				replay := *part.Reasoning.Replay
				reasoning.Replay = &replay
			}
			next.Reasoning = &reasoning
		}
		if part.ToolUse != nil {
			call := CloneToolCall(*part.ToolUse)
			next.ToolUse = &call
		}
		if part.ToolResult != nil {
			result := *part.ToolResult
			result.Content = cloneParts(part.ToolResult.Content)
			next.ToolResult = &result
		}
		if part.Media != nil {
			media := *part.Media
			next.Media = &media
		}
		if part.JSON != nil {
			value := *part.JSON
			value.Value = slices.Clone(part.JSON.Value)
			next.JSON = &value
		}
		if part.FileRef != nil {
			ref := *part.FileRef
			next.FileRef = &ref
		}
		out = append(out, next)
	}
	return out
}
