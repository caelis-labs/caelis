package eventstream

import (
	"encoding/json"
	"maps"
	"strings"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type Kind string

const (
	KindSessionUpdate      Kind = acpsdk.ClientMethodSessionUpdate
	KindRequestPermission  Kind = acpsdk.ClientMethodSessionRequestPermission
	KindNotice             Kind = "caelis/notice"
	KindParticipant        Kind = "caelis/participant"
	KindLifecycle          Kind = "caelis/lifecycle"
	KindAgentCommunication Kind = "caelis/agent_communication"
	KindApprovalReview     Kind = "caelis/approval_review"
	KindError              Kind = "caelis/error"
)

// NoticeKind identifies one normalized runtime notice independently from its
// display text.
type NoticeKind string

const (
	NoticeKindCompact       NoticeKind = "compact"
	NoticeKindCompactFailed NoticeKind = "compact_failed"
)

type Scope string

const (
	ScopeMain        Scope = "main"
	ScopeParticipant Scope = "participant"
	ScopeSubagent    Scope = "subagent"
)

// ApprovalRequestID identifies one pending Control-routed approval request.
// It is a Caelis Envelope correlation value, not an ACP wire field and not an
// event cursor. A durable SDK pause token may supply this value; otherwise the
// owning live Turn allocates it for the lifetime of that pending request.
type ApprovalRequestID string

// ParentToolRelation identifies the actual parent tool call that produced a
// scoped delegated event or owns a main-scope observer result for the same
// physical task panel. It is intentionally limited to tool-call ancestry; it
// does not model arbitrary workflow or Goal relationships.
type ParentToolRelation struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
}

// DeliveryMode classifies one Envelope's replay guarantee.
type DeliveryMode string

const (
	DeliveryCanonical DeliveryMode = "canonical"
	DeliveryMirror    DeliveryMode = "mirror"
	DeliveryTransient DeliveryMode = "transient"
)

// Delivery classifies how an Envelope reaches a client. Mode is one exclusive
// guarantee rather than a set of Boolean flags.
type Delivery struct {
	Mode DeliveryMode `json:"mode"`
}

func UsageUpdateFromSnapshot(usage session.UsageSnapshot, meta map[string]any) UsageUpdate {
	used := nonNegativeUsage(usage.TotalTokens)
	size := usage.ContextWindowTokens
	if size <= 0 {
		size = usage.TotalTokens
	}
	return UsageUpdate{
		SessionUpdate: UpdateUsage,
		Size:          nonNegativeUsage(size),
		Used:          used,
		Meta:          usageUpdateMeta(meta, usage),
	}
}

// UsageUpdateFromContextUsage restores one standard ACP context gauge without
// discarding optional cost or extension metadata retained by durable replay.
func UsageUpdateFromContextUsage(usage session.ContextUsageSnapshot, meta map[string]any) UsageUpdate {
	update := UsageUpdate{
		SessionUpdate: UpdateUsage,
		Size:          usage.Size,
		Used:          usage.Used,
		Meta:          maps.Clone(meta),
	}
	for key, raw := range usage.Meta {
		var value any
		if json.Unmarshal(raw, &value) == nil {
			if update.Meta == nil {
				update.Meta = map[string]any{}
			}
			update.Meta[key] = value
		}
	}
	if usage.Cost != nil {
		update.Cost = &acpsdk.Cost{
			Amount: usage.Cost.Amount, Currency: strings.TrimSpace(usage.Cost.Currency),
			Meta: cloneRawMessageMap(usage.Cost.Meta),
		}
	}
	return update
}

func usageSnapshotFromUpdate(update UsageUpdate) *session.UsageSnapshot {
	usage := usageSnapshotFromMeta(update.Meta)
	projectedUsage := usage != nil
	if usage == nil && update.Used == 0 && update.Size == 0 {
		return nil
	}
	if usage == nil {
		usage = &session.UsageSnapshot{}
	}
	if usage.TotalTokens == 0 && update.Used <= uint64(maxInt()) {
		usage.TotalTokens = int(update.Used)
	}
	if !projectedUsage && usage.ContextWindowTokens == 0 && update.Size <= uint64(maxInt()) {
		usage.ContextWindowTokens = int(update.Size)
	}
	if usageSnapshotEmpty(*usage) {
		return nil
	}
	return usage
}

func nonNegativeUsage(value int) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func maxInt() int { return int(^uint(0) >> 1) }

func UsageSnapshotFromEnvelope(env Envelope) *session.UsageSnapshot {
	if env.Kind != KindSessionUpdate {
		return nil
	}
	update, ok := env.Update.(UsageUpdate)
	if !ok {
		return nil
	}
	if UsageEnvelopeReplacesContext(env) {
		return usageSnapshotFromStandardUpdate(update)
	}
	return usageSnapshotFromUpdate(update)
}

func usageSnapshotFromStandardUpdate(update UsageUpdate) *session.UsageSnapshot {
	usage := &session.UsageSnapshot{}
	if update.Used <= uint64(maxInt()) {
		usage.TotalTokens = int(update.Used)
	}
	if update.Size <= uint64(maxInt()) {
		usage.ContextWindowTokens = int(update.Size)
	}
	if usageSnapshotEmpty(*usage) {
		return nil
	}
	return usage
}

// UsageSemantics identifies the Control-owned reduction rule for a usage
// update. It lives beside the ACP payload because peer-owned _meta cannot grant
// product semantics.
type UsageSemantics string

const (
	UsageSemanticsContextGauge  UsageSemantics = "context_gauge"
	UsageSemanticsProviderUsage UsageSemantics = "provider_usage"
)

// UsageEnvelopeReplacesContext reports whether a usage Envelope is the
// standard ACP replaceable context gauge. Empty semantics retains the legacy
// provider-usage merge behavior for Control v1 peers predating this field.
func UsageEnvelopeReplacesContext(env Envelope) bool {
	return env.UsageSemantics == UsageSemanticsContextGauge
}

type Envelope struct {
	Kind         Kind          `json:"kind"`
	Cursor       string        `json:"cursor,omitempty"`
	EventID      string        `json:"event_id,omitempty"`
	ProjectionID string        `json:"projection_id,omitempty"`
	Position     *FeedPosition `json:"position,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	HandleID     string        `json:"handle_id,omitempty"`
	RunID        string        `json:"run_id,omitempty"`
	TurnID       string        `json:"turn_id,omitempty"`
	// ActivityID identifies the Control-owned execution activity represented by
	// a Task-stream Envelope. It is empty on ordinary Session feed Envelopes.
	ActivityID string    `json:"activity_id,omitempty"`
	OccurredAt time.Time `json:"occurred_at,omitempty"`

	Scope          Scope               `json:"scope,omitempty"`
	ScopeID        string              `json:"scope_id,omitempty"`
	Actor          string              `json:"actor,omitempty"`
	ParticipantID  string              `json:"participant_id,omitempty"`
	Final          bool                `json:"final,omitempty"`
	UsageSemantics UsageSemantics      `json:"usage_semantics,omitempty"`
	ParentTool     *ParentToolRelation `json:"parent_tool,omitempty"`
	Delivery       *Delivery           `json:"delivery,omitempty"`
	// ApprovalRequestID is required to resolve a request_permission Envelope
	// through the Caelis Control command. It deliberately sits beside the ACP
	// payload so standard RequestPermissionRequest wire shape remains unchanged.
	ApprovalRequestID ApprovalRequestID `json:"approval_request_id,omitempty"`

	Update     Update                    `json:"update,omitempty"`
	Permission *RequestPermissionRequest `json:"permission,omitempty"`
	Notice     string                    `json:"notice,omitempty"`
	NoticeKind NoticeKind                `json:"notice_kind,omitempty"`

	ApprovalReview     *ApprovalReview     `json:"approval_review,omitempty"`
	Participant        *Participant        `json:"participant,omitempty"`
	Lifecycle          *Lifecycle          `json:"lifecycle,omitempty"`
	AgentCommunication *AgentCommunication `json:"agent_communication,omitempty"`

	Meta  map[string]any `json:"_meta,omitempty"`
	Err   error          `json:"-"`
	Error string         `json:"error,omitempty"`
}

// UnmarshalJSON restores the typed Update union when an Envelope is read from
// a Control-owned cache such as the Session spool. Transport codecs may apply
// additional numeric normalization, but plain JSON round trips must not turn
// Update into an un-decodable interface value.
func (e *Envelope) UnmarshalJSON(raw []byte) error {
	type envelopeAlias Envelope
	decoded := envelopeAlias{}
	wire := struct {
		Update json.RawMessage `json:"update"`
		*envelopeAlias
	}{envelopeAlias: &decoded}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	if len(wire.Update) > 0 && string(wire.Update) != "null" {
		update, err := DecodeUpdateJSON(wire.Update)
		if err != nil {
			return err
		}
		decoded.Update = update
	}
	*e = Envelope(decoded)
	return nil
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

// ActorIdentity is the typed sender identity attached to an Agent
// communication. It is projected from the trusted durable Event.Actor.
type ActorIdentity struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
	Role string `json:"role,omitempty"`
	Name string `json:"name,omitempty"`
}

// HasIdentity reports whether the actor can be distinguished from another
// sender without interpreting its role or kind as identity.
func (a ActorIdentity) HasIdentity() bool {
	return strings.TrimSpace(a.ID) != "" || strings.TrimSpace(a.Name) != ""
}

// AgentCommunication is one explicitly internal Agent-to-Agent message.
type AgentCommunication struct {
	Source ActorIdentity `json:"source"`
	Text   string        `json:"text"`
}

const (
	LifecycleStateRunning     = "running"
	LifecycleStateCompleted   = "completed"
	LifecycleStateFailed      = "failed"
	LifecycleStateInterrupted = "interrupted"
	LifecycleStateCancelled   = "cancelled"
	LifecycleStateTerminated  = "terminated"
	LifecycleStateUnknown     = "unknown_outcome"
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
	return TurnLifecycle(handleID, runID, turnID, LifecycleStateCompleted, "", string(acpsdk.StopReasonEndTurn), occurredAt)
}

func TurnFailed(handleID string, runID string, turnID string, reason string, occurredAt time.Time) Envelope {
	return TurnLifecycle(handleID, runID, turnID, LifecycleStateFailed, reason, "", occurredAt)
}

func TurnCancelled(handleID string, runID string, turnID string, reason string, occurredAt time.Time) Envelope {
	return TurnLifecycle(handleID, runID, turnID, LifecycleStateCancelled, reason, string(acpsdk.StopReasonCancelled), occurredAt)
}

// IsTurnTerminalLifecycle reports whether an Envelope closes one main Turn.
// Other domain lifecycles may use the same terminal states; in particular, an
// approval settlement carries ApprovalRequestID and must not end its owning
// Turn. An empty Scope is accepted for unstamped Runtime output.
func IsTurnTerminalLifecycle(env Envelope) bool {
	if env.Kind != KindLifecycle || env.Lifecycle == nil || !IsTerminalLifecycleState(env.Lifecycle.State) {
		return false
	}
	if env.Scope != "" && env.Scope != ScopeMain {
		return false
	}
	return strings.TrimSpace(string(env.ApprovalRequestID)) == ""
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
	case LifecycleStateCompleted, LifecycleStateFailed, LifecycleStateInterrupted, LifecycleStateCancelled, LifecycleStateTerminated, LifecycleStateUnknown, "canceled":
		return true
	default:
		return false
	}
}

func CloneEnvelope(in Envelope) Envelope {
	out := in
	out.Position = CloneFeedPosition(in.Position)
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
		permission.Options = append([]acpsdk.PermissionOption(nil), in.Permission.Options...)
		for index := range permission.Options {
			permission.Options[index].Meta = cloneRawMessageMap(permission.Options[index].Meta)
		}
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
	if in.AgentCommunication != nil {
		communication := *in.AgentCommunication
		out.AgentCommunication = &communication
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

func usageUpdateMeta(meta map[string]any, usage session.UsageSnapshot) map[string]any {
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

func usageSnapshotFromMeta(meta map[string]any) *session.UsageSnapshot {
	usageMeta := mapAt(mapAt(meta, "caelis"), "usage")
	if len(usageMeta) == 0 {
		return nil
	}
	usage := session.UsageSnapshot{
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

func usageSnapshotEmpty(usage session.UsageSnapshot) bool {
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

func CloneUpdate(update Update) Update {
	switch typed := update.(type) {
	case nil:
		return nil
	case ContentChunk:
		typed.Content = cloneAny(typed.Content)
		typed.Meta = cloneAnyMap(typed.Meta)
		return typed
	case ToolCall:
		typed.RawInput = cloneAny(typed.RawInput)
		typed.RawOutput = cloneAny(typed.RawOutput)
		typed.Content = cloneToolCallContent(typed.Content)
		typed.Locations = append([]ToolCallLocation(nil), typed.Locations...)
		typed.Meta = cloneAnyMap(typed.Meta)
		return typed
	case ToolCallUpdate:
		typed.Title = cloneStringPtr(typed.Title)
		typed.Kind = cloneStringPtr(typed.Kind)
		typed.Status = cloneStringPtr(typed.Status)
		typed.RawInput = cloneAny(typed.RawInput)
		typed.RawOutput = cloneAny(typed.RawOutput)
		typed.Content = cloneToolCallContent(typed.Content)
		typed.Locations = append([]ToolCallLocation(nil), typed.Locations...)
		typed.Meta = cloneAnyMap(typed.Meta)
		return typed
	case PlanUpdate:
		typed.Entries = append([]PlanEntry(nil), typed.Entries...)
		return typed
	case UsageUpdate:
		if typed.Cost != nil {
			cost := *typed.Cost
			cost.Meta = cloneRawMessageMap(cost.Meta)
			typed.Cost = &cost
		}
		typed.Meta = cloneAnyMap(typed.Meta)
		return typed
	case RawUpdate:
		typed.Raw = append([]byte(nil), typed.Raw...)
		return typed
	default:
		return update
	}
}

func UpdateType(update Update) string {
	if update == nil {
		return ""
	}
	return strings.TrimSpace(update.SessionUpdateType())
}

func UpdateMeta(update Update) map[string]any {
	switch typed := update.(type) {
	case ContentChunk:
		return cloneAnyMap(typed.Meta)
	case ToolCall:
		return cloneAnyMap(typed.Meta)
	case ToolCallUpdate:
		return cloneAnyMap(typed.Meta)
	case UsageUpdate:
		return cloneAnyMap(typed.Meta)
	default:
		return nil
	}
}

func cloneToolCallUpdate(in ToolCallUpdate) ToolCallUpdate {
	in.Title = cloneStringPtr(in.Title)
	in.Kind = cloneStringPtr(in.Kind)
	in.Status = cloneStringPtr(in.Status)
	in.RawInput = cloneAny(in.RawInput)
	in.RawOutput = cloneAny(in.RawOutput)
	in.Content = cloneToolCallContent(in.Content)
	in.Locations = append([]ToolCallLocation(nil), in.Locations...)
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

func cloneToolCallContent(in []ToolCallContent) []ToolCallContent {
	if in == nil {
		return nil
	}
	out := make([]ToolCallContent, 0, len(in))
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

func cloneRawMessageMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = append(json.RawMessage(nil), value...)
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
