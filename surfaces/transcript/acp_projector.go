package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type SurfaceProjector interface {
	ResolveToolName(meta map[string]any, title string, kind string) string
	ProjectToolCall(ToolProjectionInput) Event
	ProjectToolResult(ToolProjectionInput, string) (Event, bool)
	ApprovalCommandPreview(map[string]any) string
}

type ToolProjectionInput struct {
	Scope      Scope
	ScopeID    string
	Actor      string
	OccurredAt time.Time
	Meta       map[string]any

	CallID    string
	ToolName  string
	ToolKind  string
	ToolTitle string
	Status    string

	RawInput  map[string]any
	RawOutput any
	Content   []schema.ToolCallContent
	Error     bool

	GatewayProjection bool
}

func ProjectACPEventToEvents(env eventstream.Envelope, surface SurfaceProjector) []Event {
	scope := ACPEventScope(env.Scope)
	scopeID := ACPEventScopeID(env)
	occurredAt := env.OccurredAt
	meta := MergeMeta(ACPUpdateMeta(env.Update), env.Meta)
	relationDelivery := eventstream.ResolveRelationDelivery(env)
	parentToolCallID := ""
	parentToolName := ""
	if parentTool := relationDelivery.ParentTool; parentTool != nil {
		parentToolCallID = parentTool.ToolCallID
		parentToolName = parentTool.ToolName
	}
	hasParentToolMirror := relationDelivery.Delivery != nil && relationDelivery.Delivery.HasParentToolMirror
	out := make([]Event, 0, 2)
	switch env.Kind {
	case eventstream.KindSessionUpdate:
		out = append(out, projectACPSessionUpdate(env, meta, scope, scopeID, surface)...)
	case eventstream.KindNotice:
		if text := strings.TrimSpace(env.Notice); text != "" {
			out = append(out, Event{
				Kind:          EventNotice,
				Scope:         scope,
				ScopeID:       scopeID,
				Actor:         strings.TrimSpace(env.Actor),
				OccurredAt:    occurredAt,
				NarrativeKind: NarrativeNotice,
				Text:          text,
				Final:         true,
			})
		}
	case eventstream.KindParticipant:
		if env.Participant != nil && strings.TrimSpace(env.Participant.State) != "" {
			out = append(out, Event{
				Kind:       EventParticipant,
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      strings.TrimSpace(env.Actor),
				OccurredAt: occurredAt,
				State:      strings.TrimSpace(env.Participant.State),
			})
		}
	case eventstream.KindLifecycle:
		if env.Lifecycle != nil && strings.TrimSpace(env.Lifecycle.State) != "" {
			state := strings.TrimSpace(env.Lifecycle.State)
			eventMeta := attemptResetSurfaceMeta(state, meta)
			out = append(out, Event{
				Kind:       EventLifecycle,
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      strings.TrimSpace(env.Actor),
				OccurredAt: occurredAt,
				Meta:       eventMeta,
				State:      state,
			})
			if notice := attemptResetNoticeText(state, eventMeta); notice != "" {
				out = append(out, Event{
					Kind:          EventNotice,
					Scope:         scope,
					ScopeID:       scopeID,
					Actor:         strings.TrimSpace(env.Actor),
					OccurredAt:    occurredAt,
					Meta:          eventMeta,
					NarrativeKind: NarrativeNotice,
					NoticeKind:    NoticeKindModelRetry,
					Text:          notice,
					Final:         true,
				})
			}
		}
	case eventstream.KindApprovalReview:
		if event, ok := projectACPApprovalReview(env, scope, scopeID, surface); ok {
			out = append(out, event)
		}
	}
	for i := range out {
		out[i].TurnID = strings.TrimSpace(env.TurnID)
		out[i].AnchorToolCallID = parentToolCallID
		out[i].AnchorToolName = parentToolName
		out[i].MirroredToParentTool = hasParentToolMirror
	}
	return out
}

func attemptResetSurfaceMeta(state string, meta map[string]any) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(state), "attempt_reset") {
		return meta
	}
	caelis, ok := meta["caelis"].(map[string]any)
	if !ok {
		return meta
	}
	runtimeMeta, ok := caelis["runtime"].(map[string]any)
	if !ok {
		return meta
	}
	attemptMeta, ok := runtimeMeta["attempt_reset"].(map[string]any)
	if !ok {
		return meta
	}
	if _, ok := attemptMeta["cause"]; !ok {
		return meta
	}
	// Legacy/replay defense: current runtime no longer writes provider causes
	// into attempt_reset meta, but older streams may still carry them.
	out := CloneAnyMap(meta)
	caelisOut := CloneAnyMap(caelis)
	runtimeOut := CloneAnyMap(runtimeMeta)
	attemptOut := CloneAnyMap(attemptMeta)
	delete(attemptOut, "cause")
	runtimeOut["attempt_reset"] = attemptOut
	caelisOut["runtime"] = runtimeOut
	out["caelis"] = caelisOut
	return out
}

func attemptResetNoticeText(state string, meta map[string]any) string {
	if !strings.EqualFold(strings.TrimSpace(state), "attempt_reset") {
		return ""
	}
	if !MetaBool(meta, "caelis", "runtime", "attempt_reset", "retrying") {
		return ""
	}
	attempt := MetaInt(meta, "caelis", "runtime", "attempt_reset", "attempt")
	maxRetries := MetaInt(meta, "caelis", "runtime", "attempt_reset", "max_retries")
	retryDelayMillis := MetaInt(meta, "caelis", "runtime", "attempt_reset", "retry_delay_ms")
	parts := make([]string, 0, 2)
	switch {
	case attempt > 0 && maxRetries > 0:
		parts = append(parts, fmt.Sprintf("%d/%d", attempt, maxRetries))
	case attempt > 0:
		parts = append(parts, fmt.Sprintf("attempt %d", attempt))
	}
	if retryDelay := retryNoticeDelayText(retryDelayMillis); retryDelay != "" {
		parts = append(parts, retryDelay)
	}
	if len(parts) == 0 {
		return "Retrying model request"
	}
	return "Retrying model request (" + strings.Join(parts, ", ") + ")"
}

func retryNoticeDelayText(milliseconds int) string {
	if milliseconds <= 0 {
		return ""
	}
	delay := time.Duration(milliseconds) * time.Millisecond
	rounded := delay.Round(time.Second)
	if rounded < time.Second {
		rounded = time.Second
	}
	if rounded < time.Minute {
		return fmt.Sprintf("retry in %ds", int(rounded/time.Second))
	}
	minutes := int(rounded / time.Minute)
	seconds := int((rounded % time.Minute) / time.Second)
	if seconds == 0 {
		return fmt.Sprintf("retry in %dm", minutes)
	}
	return fmt.Sprintf("retry in %dm%02ds", minutes, seconds)
}

func projectACPSessionUpdate(env eventstream.Envelope, meta map[string]any, scope Scope, scopeID string, surface SurfaceProjector) []Event {
	switch update := env.Update.(type) {
	case schema.ContentChunk:
		return projectACPContentChunk(env, update, meta, scope, scopeID)
	case schema.ToolCall:
		if surface == nil || ToolIsPlan(update.Title, update.Kind) {
			return nil
		}
		return []Event{surface.ProjectToolCall(ToolProjectionInput{
			Scope:      scope,
			ScopeID:    scopeID,
			Actor:      strings.TrimSpace(env.Actor),
			OccurredAt: env.OccurredAt,
			Meta:       meta,
			CallID:     update.ToolCallID,
			ToolName:   acpUpdateToolName(meta, update.Title, update.Kind, surface),
			ToolKind:   update.Kind,
			ToolTitle:  update.Title,
			Status:     update.Status,
			RawInput:   RawMap(update.RawInput),
			Content:    update.Content,
		})}
	case schema.ToolCallUpdate:
		if surface == nil {
			return nil
		}
		title := StringFromPtr(update.Title)
		kind := StringFromPtr(update.Kind)
		if ToolIsPlan(title, kind) {
			return nil
		}
		event, ok := surface.ProjectToolResult(ToolProjectionInput{
			Scope:             scope,
			ScopeID:           scopeID,
			Actor:             strings.TrimSpace(env.Actor),
			OccurredAt:        env.OccurredAt,
			Meta:              meta,
			CallID:            update.ToolCallID,
			ToolName:          acpUpdateToolName(meta, title, kind, surface),
			ToolKind:          kind,
			ToolTitle:         title,
			Status:            StringFromPtr(update.Status),
			RawInput:          RawMap(update.RawInput),
			RawOutput:         update.RawOutput,
			Content:           update.Content,
			Error:             ToolUpdateError(update),
			GatewayProjection: GatewayProjection(meta),
		}, "in_progress")
		if !ok {
			return nil
		}
		return []Event{event}
	case schema.PlanUpdate:
		entries := make([]PlanEntry, 0, len(update.Entries))
		for _, entry := range update.Entries {
			entries = append(entries, PlanEntry{Content: entry.Content, Status: entry.Status})
		}
		if len(entries) == 0 {
			return nil
		}
		return []Event{{
			Kind:        EventPlan,
			Scope:       scope,
			ScopeID:     scopeID,
			Actor:       strings.TrimSpace(env.Actor),
			OccurredAt:  env.OccurredAt,
			PlanEntries: entries,
		}}
	case schema.UsageUpdate:
		usage := eventstream.UsageSnapshotFromEnvelope(env)
		if usage == nil {
			return nil
		}
		return []Event{{
			Kind:       EventUsage,
			Scope:      scope,
			ScopeID:    scopeID,
			Actor:      strings.TrimSpace(env.Actor),
			OccurredAt: env.OccurredAt,
			Usage:      usage,
		}}
	default:
		return nil
	}
}

func projectACPContentChunk(env eventstream.Envelope, update schema.ContentChunk, meta map[string]any, scope Scope, scopeID string) []Event {
	text := ProtocolTextContent(update.Content)
	if text == "" {
		return nil
	}
	switch strings.TrimSpace(update.SessionUpdate) {
	case schema.UpdateUserMessage:
		if scope != ScopeMain && scope != ScopeParticipant {
			return nil
		}
		return []Event{{
			Kind:          EventNarrative,
			Scope:         scope,
			ScopeID:       scopeID,
			Actor:         strings.TrimSpace(env.Actor),
			OccurredAt:    env.OccurredAt,
			Meta:          meta,
			NarrativeKind: NarrativeUser,
			Text:          strings.TrimSpace(text),
			Final:         true,
		}}
	case schema.UpdateAgentMessage:
		return []Event{{
			Kind:          EventNarrative,
			Scope:         scope,
			ScopeID:       scopeID,
			Actor:         strings.TrimSpace(env.Actor),
			OccurredAt:    env.OccurredAt,
			NarrativeKind: NarrativeAssistant,
			Text:          text,
			Final:         env.Final,
		}}
	case schema.UpdateAgentThought:
		return []Event{{
			Kind:          EventNarrative,
			Scope:         scope,
			ScopeID:       scopeID,
			Actor:         strings.TrimSpace(env.Actor),
			OccurredAt:    env.OccurredAt,
			NarrativeKind: NarrativeReasoning,
			Text:          text,
			Final:         env.Final,
		}}
	case schema.UpdateCompact:
		return []Event{{
			Kind:          EventNotice,
			Scope:         scope,
			ScopeID:       scopeID,
			Actor:         strings.TrimSpace(env.Actor),
			OccurredAt:    env.OccurredAt,
			NarrativeKind: NarrativeNotice,
			NoticeKind:    NoticeKindCompact,
			Text:          CompactNoticeLabel,
			Final:         true,
		}}
	default:
		return nil
	}
}

func projectACPApprovalReview(env eventstream.Envelope, scope Scope, scopeID string, surface SurfaceProjector) (Event, bool) {
	if env.ApprovalReview == nil {
		return Event{}, false
	}
	text := acpApprovalReviewDisplayText(*env.ApprovalReview)
	if text == "" {
		return Event{}, false
	}
	preview := ""
	if surface != nil {
		preview = surface.ApprovalCommandPreview(env.ApprovalReview.RawInput)
	}
	display := ApprovalReviewDisplayParts(env.ApprovalReview.Status, env.ApprovalReview.Risk, env.ApprovalReview.Authorization, text)
	return Event{
		Kind:            EventApproval,
		Scope:           scope,
		ScopeID:         scopeID,
		Actor:           strings.TrimSpace(env.Actor),
		OccurredAt:      env.OccurredAt,
		ToolCallID:      strings.TrimSpace(env.ApprovalReview.ToolCallID),
		ApprovalTool:    strings.TrimSpace(env.ApprovalReview.ToolName),
		ApprovalCommand: preview,
		ApprovalStatus:  display.Status,
		ApprovalRisk:    display.Risk,
		ApprovalAuth:    display.Authorization,
		ApprovalText:    text,
		Final:           true,
	}, true
}

func acpApprovalReviewDisplayText(review eventstream.ApprovalReview) string {
	switch strings.ToLower(strings.TrimSpace(review.Status)) {
	case "approved", "denied", "timed_out", "failed":
		return FirstNonEmpty(strings.TrimSpace(review.Text), "Automatic approval review "+strings.TrimSpace(review.Status))
	default:
		return strings.TrimSpace(review.Text)
	}
}

func ACPEventScope(scope eventstream.Scope) Scope {
	switch scope {
	case eventstream.ScopeParticipant:
		return ScopeParticipant
	case eventstream.ScopeSubagent:
		return ScopeSubagent
	default:
		return ScopeMain
	}
}

func ACPEventScopeID(env eventstream.Envelope) string {
	if scopeID := strings.TrimSpace(env.ScopeID); scopeID != "" {
		return scopeID
	}
	if sessionID := strings.TrimSpace(env.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(env.TurnID)
}

func ACPUpdateMeta(update schema.Update) map[string]any {
	switch typed := update.(type) {
	case schema.ToolCall:
		return CloneAnyMap(typed.Meta)
	case schema.ToolCallUpdate:
		return CloneAnyMap(typed.Meta)
	default:
		return nil
	}
}

func ToolIsPlan(values ...string) bool {
	for _, value := range values {
		if canonical, ok := names.Resolve(value); ok && canonical == names.Plan {
			return true
		}
	}
	return false
}

func ToolUpdateError(update schema.ToolCallUpdate) bool {
	status := strings.ToLower(strings.TrimSpace(StringFromPtr(update.Status)))
	if status == "failed" || status == "error" {
		return true
	}
	rawOutput := RawMap(update.RawOutput)
	if value, ok := rawOutput["is_error"].(bool); ok && value {
		return true
	}
	if value, ok := rawOutput["error"].(string); ok && strings.TrimSpace(value) != "" && status != "completed" {
		return true
	}
	return false
}

func RawMap(raw any) map[string]any {
	return schema.NormalizeRawMap(raw)
}

func GatewayProjection(meta map[string]any) bool {
	return strings.EqualFold(MetaString(meta, "caelis", "bridge", "source"), "gateway_projection")
}

func acpUpdateToolName(meta map[string]any, title string, kind string, surface SurfaceProjector) string {
	if surface != nil {
		if name := surface.ResolveToolName(meta, title, kind); name != "" {
			return name
		}
	}
	if name := MetaString(meta, "caelis", "runtime", "tool", "name"); name != "" {
		return name
	}
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(title)
}

func StringFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func MetaString(meta map[string]any, path ...string) string {
	if len(meta) == 0 {
		return ""
	}
	var current any = meta
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[strings.TrimSpace(key)]
	}
	value, _ := current.(string)
	return strings.TrimSpace(value)
}

func MetaBool(meta map[string]any, path ...string) bool {
	if len(meta) == 0 {
		return false
	}
	var current any = meta
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current = mapped[strings.TrimSpace(key)]
	}
	value, _ := current.(bool)
	return value
}

func MetaInt(meta map[string]any, path ...string) int {
	if len(meta) == 0 {
		return 0
	}
	var current any = meta
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return 0
		}
		current = mapped[strings.TrimSpace(key)]
	}
	switch value := current.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

func MergeMeta(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 {
		return CloneAnyMap(overlay)
	}
	out := CloneAnyMap(base)
	for key, value := range overlay {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = MergeMeta(baseMap, overlayMap)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func CloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func ProtocolTextContent(raw any) string {
	switch typed := raw.(type) {
	case nil:
		return ""
	case map[string]any:
		if typ, _ := typed["type"].(string); !strings.EqualFold(strings.TrimSpace(typ), "text") {
			return ""
		}
		text, _ := typed["text"].(string)
		return text
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return ""
		}
		var decoded struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &decoded); err != nil {
			return ""
		}
		if !strings.EqualFold(strings.TrimSpace(decoded.Type), "text") {
			return ""
		}
		return decoded.Text
	}
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
