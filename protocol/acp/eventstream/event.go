package eventstream

import (
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type Kind string

const (
	KindSessionUpdate     Kind = schema.MethodSessionUpdate
	KindRequestPermission Kind = schema.MethodSessionReqPermission
	KindNotice            Kind = "caelis/notice"
	KindParticipant       Kind = "caelis/participant"
	KindLifecycle         Kind = "caelis/lifecycle"
	KindApprovalReview    Kind = "caelis/approval_review"
	KindUsage             Kind = "caelis/usage"
	KindError             Kind = "caelis/error"
)

type Scope string

const (
	ScopeMain        Scope = "main"
	ScopeParticipant Scope = "participant"
	ScopeSubagent    Scope = "subagent"
)

type UsageSnapshot struct {
	PromptTokens      int `json:"prompt_tokens,omitempty"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	CompletionTokens  int `json:"completion_tokens,omitempty"`
	ReasoningTokens   int `json:"reasoning_tokens,omitempty"`
	TotalTokens       int `json:"total_tokens,omitempty"`
}

type Envelope struct {
	Kind       Kind      `json:"kind"`
	Cursor     string    `json:"cursor,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	HandleID   string    `json:"handle_id,omitempty"`
	RunID      string    `json:"run_id,omitempty"`
	TurnID     string    `json:"turn_id,omitempty"`
	OccurredAt time.Time `json:"occurred_at,omitempty"`

	Scope         Scope  `json:"scope,omitempty"`
	ScopeID       string `json:"scope_id,omitempty"`
	Actor         string `json:"actor,omitempty"`
	ParticipantID string `json:"participant_id,omitempty"`
	Final         bool   `json:"final,omitempty"`

	Update     schema.Update                    `json:"update,omitempty"`
	Permission *schema.RequestPermissionRequest `json:"permission,omitempty"`
	Notice     string                           `json:"notice,omitempty"`

	ApprovalReview *ApprovalReview `json:"approval_review,omitempty"`
	Participant    *Participant    `json:"participant,omitempty"`
	Lifecycle      *Lifecycle      `json:"lifecycle,omitempty"`
	Usage          *UsageSnapshot  `json:"usage,omitempty"`

	Meta  map[string]any `json:"_meta,omitempty"`
	Err   error          `json:"-"`
	Error string         `json:"error,omitempty"`
}

type ApprovalReview struct {
	ToolCallID    string         `json:"tool_call_id,omitempty"`
	ToolName      string         `json:"tool_name,omitempty"`
	RawInput      map[string]any `json:"raw_input,omitempty"`
	Status        string         `json:"status,omitempty"`
	Text          string         `json:"text,omitempty"`
	Risk          string         `json:"risk,omitempty"`
	Authorization string         `json:"authorization,omitempty"`
}

type Participant struct {
	State string `json:"state,omitempty"`
}

type Lifecycle struct {
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func Error(err error) Envelope {
	text := ""
	if err != nil {
		text = err.Error()
	}
	return Envelope{Kind: KindError, Err: err, Error: strings.TrimSpace(text)}
}

func CloneEnvelope(in Envelope) Envelope {
	out := in
	out.Meta = cloneAnyMap(in.Meta)
	if in.Permission != nil {
		permission := *in.Permission
		permission.Options = append([]schema.PermissionOption(nil), in.Permission.Options...)
		permission.ToolCall = cloneToolCallUpdate(in.Permission.ToolCall)
		out.Permission = &permission
	}
	if in.ApprovalReview != nil {
		approval := *in.ApprovalReview
		approval.RawInput = cloneAnyMap(in.ApprovalReview.RawInput)
		out.ApprovalReview = &approval
	}
	if in.Participant != nil {
		participant := *in.Participant
		out.Participant = &participant
	}
	if in.Lifecycle != nil {
		lifecycle := *in.Lifecycle
		out.Lifecycle = &lifecycle
	}
	if in.Usage != nil {
		usage := *in.Usage
		out.Usage = &usage
	}
	out.Update = CloneUpdate(in.Update)
	return out
}

func CloneUpdate(update schema.Update) schema.Update {
	switch typed := update.(type) {
	case nil:
		return nil
	case schema.ContentChunk:
		return typed
	case schema.ToolCall:
		typed.RawInput = cloneAny(typed.RawInput)
		typed.RawOutput = cloneAny(typed.RawOutput)
		typed.Content = cloneToolCallContent(typed.Content)
		typed.Locations = append([]schema.ToolCallLocation(nil), typed.Locations...)
		typed.Meta = cloneAnyMap(typed.Meta)
		return typed
	case schema.ToolCallUpdate:
		typed.RawInput = cloneAny(typed.RawInput)
		typed.RawOutput = cloneAny(typed.RawOutput)
		typed.Content = cloneToolCallContent(typed.Content)
		typed.Locations = append([]schema.ToolCallLocation(nil), typed.Locations...)
		typed.Meta = cloneAnyMap(typed.Meta)
		return typed
	case schema.PlanUpdate:
		typed.Entries = append([]schema.PlanEntry(nil), typed.Entries...)
		return typed
	case schema.RawUpdate:
		typed.Raw = append([]byte(nil), typed.Raw...)
		return typed
	default:
		return update
	}
}

func UpdateType(update schema.Update) string {
	if update == nil {
		return ""
	}
	return strings.TrimSpace(update.SessionUpdateType())
}

func UpdateMeta(update schema.Update) map[string]any {
	switch typed := update.(type) {
	case schema.ToolCall:
		return cloneAnyMap(typed.Meta)
	case schema.ToolCallUpdate:
		return cloneAnyMap(typed.Meta)
	default:
		return nil
	}
}

func IsError(err error, target error) bool {
	return errors.Is(err, target)
}

func cloneToolCallUpdate(in schema.ToolCallUpdate) schema.ToolCallUpdate {
	in.RawInput = cloneAny(in.RawInput)
	in.RawOutput = cloneAny(in.RawOutput)
	in.Content = cloneToolCallContent(in.Content)
	in.Locations = append([]schema.ToolCallLocation(nil), in.Locations...)
	in.Meta = cloneAnyMap(in.Meta)
	return in
}

func cloneToolCallContent(in []schema.ToolCallContent) []schema.ToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(in))
	for _, item := range in {
		copy := item
		if item.OldText != nil {
			value := *item.OldText
			copy.OldText = &value
		}
		copy.Content = cloneAny(item.Content)
		out = append(out, copy)
	}
	return out
}

func cloneAny(in any) any {
	switch typed := in.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	default:
		return in
	}
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	return maps.Clone(in)
}
