package eventstream

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type Kind string

const (
	KindSessionUpdate     Kind = schema.MethodSessionUpdate
	KindRequestPermission Kind = schema.MethodSessionReqPermission
	KindNotice            Kind = "caelis/notice"
	KindParticipant       Kind = "caelis/participant"
	KindLifecycle         Kind = "caelis/lifecycle"
	KindApprovalReview    Kind = "caelis/approval_review"
	KindError             Kind = "caelis/error"
)

type Scope string

const (
	ScopeMain        Scope = "main"
	ScopeParticipant Scope = "participant"
	ScopeSubagent    Scope = "subagent"
)

// ParentToolRelation identifies the actual parent tool call that produced a
// scoped delegated event. It is intentionally limited to tool-call ancestry;
// it does not model arbitrary workflow or Goal relationships.
type ParentToolRelation struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
}

// Delivery classifies how an Envelope reaches a client. A transient Envelope
// is live-only and has no durable replay authority. HasParentToolMirror is true
// only when the same source frame emits a parent-tool compatibility mirror;
// IsParentToolMirror identifies that parent update. The mirror fields do not
// change the standard ACP payload carried by the Envelope.
type Delivery struct {
	Transient           bool `json:"transient,omitempty"`
	HasParentToolMirror bool `json:"has_parent_tool_mirror,omitempty"`
	IsParentToolMirror  bool `json:"is_parent_tool_mirror,omitempty"`
}

type UsageSnapshot struct {
	PromptTokens        int `json:"prompt_tokens,omitempty"`
	CachedInputTokens   int `json:"cached_input_tokens,omitempty"`
	CompletionTokens    int `json:"completion_tokens,omitempty"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
	TotalTokens         int `json:"total_tokens,omitempty"`
	ContextWindowTokens int `json:"context_window_tokens,omitempty"`
}

func UsageUpdateFromSnapshot(usage UsageSnapshot, meta map[string]any) schema.UsageUpdate {
	used := usage.TotalTokens
	size := usage.ContextWindowTokens
	if size <= 0 {
		size = used
	}
	return schema.UsageUpdate{
		SessionUpdate: schema.UpdateUsage,
		Size:          size,
		Used:          used,
		Meta:          usageUpdateMeta(meta, usage),
	}
}

func UsageSnapshotFromUpdate(update schema.UsageUpdate) *UsageSnapshot {
	usage := usageSnapshotFromMeta(update.Meta)
	if usage == nil && update.Used == 0 {
		return nil
	}
	if usage == nil {
		usage = &UsageSnapshot{}
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = update.Used
	}
	if usageSnapshotEmpty(*usage) {
		return nil
	}
	return usage
}

func UsageSnapshotFromEnvelope(env Envelope) *UsageSnapshot {
	if env.Kind != KindSessionUpdate {
		return nil
	}
	update, ok := env.Update.(schema.UsageUpdate)
	if !ok {
		return nil
	}
	return UsageSnapshotFromUpdate(update)
}

type Envelope struct {
	Kind         Kind      `json:"kind"`
	Cursor       string    `json:"cursor,omitempty"`
	EventID      string    `json:"event_id,omitempty"`
	ProjectionID string    `json:"projection_id,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	HandleID     string    `json:"handle_id,omitempty"`
	RunID        string    `json:"run_id,omitempty"`
	TurnID       string    `json:"turn_id,omitempty"`
	OccurredAt   time.Time `json:"occurred_at,omitempty"`

	Scope         Scope               `json:"scope,omitempty"`
	ScopeID       string              `json:"scope_id,omitempty"`
	Actor         string              `json:"actor,omitempty"`
	ParticipantID string              `json:"participant_id,omitempty"`
	Final         bool                `json:"final,omitempty"`
	ParentTool    *ParentToolRelation `json:"parent_tool,omitempty"`
	Delivery      *Delivery           `json:"delivery,omitempty"`

	Update     schema.Update                    `json:"update,omitempty"`
	Permission *schema.RequestPermissionRequest `json:"permission,omitempty"`
	Notice     string                           `json:"notice,omitempty"`

	ApprovalReview *ApprovalReview `json:"approval_review,omitempty"`
	Participant    *Participant    `json:"participant,omitempty"`
	Lifecycle      *Lifecycle      `json:"lifecycle,omitempty"`

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
	State      string `json:"state,omitempty"`
	Reason     string `json:"reason,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
}

const (
	LifecycleStateRunning     = "running"
	LifecycleStateCompleted   = "completed"
	LifecycleStateFailed      = "failed"
	LifecycleStateInterrupted = "interrupted"
	LifecycleStateCancelled   = "cancelled"
)

func Error(err error) Envelope {
	text := ""
	if err != nil {
		text = err.Error()
	}
	return Envelope{Kind: KindError, Err: err, Error: strings.TrimSpace(text)}
}

func TurnLifecycle(handleID string, runID string, turnID string, state string, reason string, stopReason string, occurredAt time.Time) Envelope {
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	return Envelope{
		Kind:       KindLifecycle,
		HandleID:   strings.TrimSpace(handleID),
		RunID:      strings.TrimSpace(runID),
		TurnID:     strings.TrimSpace(turnID),
		OccurredAt: occurredAt,
		Scope:      ScopeMain,
		Lifecycle: &Lifecycle{
			State:      strings.TrimSpace(state),
			Reason:     strings.TrimSpace(reason),
			StopReason: strings.TrimSpace(stopReason),
		},
	}
}

func TurnCompleted(handleID string, runID string, turnID string, occurredAt time.Time) Envelope {
	return TurnLifecycle(handleID, runID, turnID, LifecycleStateCompleted, "", schema.StopReasonEndTurn, occurredAt)
}

func TurnFailed(handleID string, runID string, turnID string, reason string, occurredAt time.Time) Envelope {
	return TurnLifecycle(handleID, runID, turnID, LifecycleStateFailed, reason, "", occurredAt)
}

func TurnCancelled(handleID string, runID string, turnID string, reason string, occurredAt time.Time) Envelope {
	return TurnLifecycle(handleID, runID, turnID, LifecycleStateCancelled, reason, schema.StopReasonCancelled, occurredAt)
}

func EnsureTerminalLifecycle(events <-chan Envelope, handleID string, runID string, turnID string) <-chan Envelope {
	out := make(chan Envelope, 32)
	go func() {
		defer close(out)
		if events == nil {
			out <- TurnCompleted(handleID, runID, turnID, time.Now())
			return
		}
		terminalSeen := false
		failureReason := ""
		cancelled := false
		for env := range events {
			if terminalSeen {
				continue
			}
			if IsTerminalLifecycle(env) {
				terminalSeen = true
				out <- env
				continue
			}
			if env.Err != nil || env.Kind == KindError {
				failureReason = strings.TrimSpace(firstNonEmpty(env.Error, errorString(env.Err)))
				cancelled = IsCancelledReason(failureReason)
			}
			out <- env
		}
		if terminalSeen {
			return
		}
		switch {
		case cancelled:
			out <- TurnCancelled(handleID, runID, turnID, failureReason, time.Now())
		case failureReason != "":
			out <- TurnFailed(handleID, runID, turnID, failureReason, time.Now())
		default:
			out <- TurnCompleted(handleID, runID, turnID, time.Now())
		}
	}()
	return out
}

func IsTerminalLifecycle(env Envelope) bool {
	if env.Kind != KindLifecycle || env.Lifecycle == nil {
		return false
	}
	return IsTerminalLifecycleState(env.Lifecycle.State)
}

func IsCancelledReason(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return reason == "context canceled" ||
		strings.Contains(reason, "context canceled") ||
		strings.Contains(reason, "cancelled") ||
		strings.Contains(reason, "canceled")
}

func IsTerminalLifecycleState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case LifecycleStateCompleted, LifecycleStateFailed, LifecycleStateInterrupted, LifecycleStateCancelled, "canceled", "terminated":
		return true
	default:
		return false
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func CloneEnvelope(in Envelope) Envelope {
	out := in
	out.Meta = cloneAnyMap(in.Meta)
	if in.ParentTool != nil {
		parentTool := *in.ParentTool
		out.ParentTool = &parentTool
	}
	if in.Delivery != nil {
		delivery := *in.Delivery
		out.Delivery = &delivery
	}
	if in.Permission != nil {
		permission := *in.Permission
		permission.Options = append([]schema.PermissionOption(nil), in.Permission.Options...)
		permission.ToolCall = cloneToolCallUpdate(in.Permission.ToolCall)
		permission.Meta = cloneAnyMap(in.Permission.Meta)
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
	out.Update = CloneUpdate(in.Update)
	return out
}

// CloneEnvelopes deep-clones a slice of envelopes.
func CloneEnvelopes(in []Envelope) []Envelope {
	if len(in) == 0 {
		return nil
	}
	out := make([]Envelope, 0, len(in))
	for _, env := range in {
		out = append(out, CloneEnvelope(env))
	}
	return out
}

func usageUpdateMeta(meta map[string]any, usage UsageSnapshot) map[string]any {
	out := cloneAnyMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis := cloneAnyMap(mapAt(out, "caelis"))
	if caelis == nil {
		caelis = map[string]any{}
	}
	caelis["version"] = 1
	usageMeta := map[string]any{}
	setPositiveInt(usageMeta, "prompt_tokens", usage.PromptTokens)
	setPositiveInt(usageMeta, "cached_input_tokens", usage.CachedInputTokens)
	setPositiveInt(usageMeta, "completion_tokens", usage.CompletionTokens)
	setPositiveInt(usageMeta, "reasoning_tokens", usage.ReasoningTokens)
	setPositiveInt(usageMeta, "total_tokens", usage.TotalTokens)
	setPositiveInt(usageMeta, "context_window_tokens", usage.ContextWindowTokens)
	if len(usageMeta) > 0 {
		caelis["usage"] = usageMeta
	} else {
		delete(caelis, "usage")
	}
	out["caelis"] = caelis
	return out
}

func usageSnapshotFromMeta(meta map[string]any) *UsageSnapshot {
	usageMeta := mapAt(mapAt(meta, "caelis"), "usage")
	if len(usageMeta) == 0 {
		return nil
	}
	usage := UsageSnapshot{
		PromptTokens:        intFromAny(usageMeta["prompt_tokens"]),
		CachedInputTokens:   intFromAny(usageMeta["cached_input_tokens"]),
		CompletionTokens:    intFromAny(usageMeta["completion_tokens"]),
		ReasoningTokens:     intFromAny(usageMeta["reasoning_tokens"]),
		TotalTokens:         intFromAny(usageMeta["total_tokens"]),
		ContextWindowTokens: intFromAny(usageMeta["context_window_tokens"]),
	}
	if usageSnapshotEmpty(usage) {
		return nil
	}
	return &usage
}

func usageSnapshotEmpty(usage UsageSnapshot) bool {
	return usage.PromptTokens == 0 &&
		usage.CachedInputTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.TotalTokens == 0 &&
		usage.ContextWindowTokens == 0
}

func setPositiveInt(values map[string]any, key string, value int) {
	if value > 0 {
		values[key] = value
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	default:
		return 0
	}
}

func CloneUpdate(update schema.Update) schema.Update {
	switch typed := update.(type) {
	case nil:
		return nil
	case schema.ContentChunk:
		typed.Content = cloneAny(typed.Content)
		typed.Meta = cloneAnyMap(typed.Meta)
		return typed
	case schema.ToolCall:
		typed.RawInput = cloneAny(typed.RawInput)
		typed.RawOutput = cloneAny(typed.RawOutput)
		typed.Content = cloneToolCallContent(typed.Content)
		typed.Locations = append([]schema.ToolCallLocation(nil), typed.Locations...)
		typed.Meta = cloneAnyMap(typed.Meta)
		return typed
	case schema.ToolCallUpdate:
		typed.Title = cloneStringPtr(typed.Title)
		typed.Kind = cloneStringPtr(typed.Kind)
		typed.Status = cloneStringPtr(typed.Status)
		typed.RawInput = cloneAny(typed.RawInput)
		typed.RawOutput = cloneAny(typed.RawOutput)
		typed.Content = cloneToolCallContent(typed.Content)
		typed.Locations = append([]schema.ToolCallLocation(nil), typed.Locations...)
		typed.Meta = cloneAnyMap(typed.Meta)
		return typed
	case schema.PlanUpdate:
		typed.Entries = append([]schema.PlanEntry(nil), typed.Entries...)
		return typed
	case schema.UsageUpdate:
		if typed.Cost != nil {
			cost := *typed.Cost
			typed.Cost = &cost
		}
		typed.Meta = cloneAnyMap(typed.Meta)
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
	case schema.ContentChunk:
		return cloneAnyMap(typed.Meta)
	case schema.ToolCall:
		return cloneAnyMap(typed.Meta)
	case schema.ToolCallUpdate:
		return cloneAnyMap(typed.Meta)
	case schema.UsageUpdate:
		return cloneAnyMap(typed.Meta)
	default:
		return nil
	}
}

func IsError(err error, target error) bool {
	return errors.Is(err, target)
}

func cloneToolCallUpdate(in schema.ToolCallUpdate) schema.ToolCallUpdate {
	in.Title = cloneStringPtr(in.Title)
	in.Kind = cloneStringPtr(in.Kind)
	in.Status = cloneStringPtr(in.Status)
	in.RawInput = cloneAny(in.RawInput)
	in.RawOutput = cloneAny(in.RawOutput)
	in.Content = cloneToolCallContent(in.Content)
	in.Locations = append([]schema.ToolCallLocation(nil), in.Locations...)
	in.Meta = cloneAnyMap(in.Meta)
	return in
}

func cloneStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	out := *in
	return &out
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
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return in
	}
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := maps.Clone(in)
	for key, value := range out {
		out[key] = cloneAny(value)
	}
	return out
}

func mapAt(values map[string]any, key string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out, _ := values[key].(map[string]any)
	return out
}
