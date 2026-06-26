package transcript

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
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
	anchorToolCallID := MetaString(meta, "caelis", "runtime", "stream", "parent_call_id")
	anchorToolName := MetaString(meta, "caelis", "runtime", "stream", "parent_tool")
	mirroredToParentTool := MetaBool(meta, "caelis", "runtime", "stream", "mirrored_to_parent_tool")
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
			out = append(out, Event{
				Kind:       EventLifecycle,
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      strings.TrimSpace(env.Actor),
				OccurredAt: occurredAt,
				State:      strings.TrimSpace(env.Lifecycle.State),
			})
		}
	case eventstream.KindApprovalReview:
		if event, ok := projectACPApprovalReview(env, scope, scopeID, surface); ok {
			out = append(out, event)
		}
	case eventstream.KindUsage:
		if env.Usage != nil {
			usage := *env.Usage
			out = append(out, Event{
				Kind:       EventUsage,
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      strings.TrimSpace(env.Actor),
				OccurredAt: occurredAt,
				Usage:      &usage,
			})
		}
	}
	for i := range out {
		out[i].TurnID = strings.TrimSpace(env.TurnID)
		out[i].AnchorToolCallID = anchorToolCallID
		out[i].AnchorToolName = anchorToolName
		out[i].MirroredToParentTool = mirroredToParentTool
	}
	return out
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
	return Event{
		Kind:            EventApproval,
		Scope:           scope,
		ScopeID:         scopeID,
		Actor:           strings.TrimSpace(env.Actor),
		OccurredAt:      env.OccurredAt,
		ToolCallID:      strings.TrimSpace(env.ApprovalReview.ToolCallID),
		ApprovalTool:    strings.TrimSpace(env.ApprovalReview.ToolName),
		ApprovalCommand: preview,
		ApprovalStatus:  strings.TrimSpace(env.ApprovalReview.Status),
		ApprovalRisk:    FirstNonEmpty(strings.TrimSpace(env.ApprovalReview.Risk), ApprovalReviewValueFromText(text, "risk")),
		ApprovalAuth:    FirstNonEmpty(strings.TrimSpace(env.ApprovalReview.Authorization), ApprovalReviewValueFromText(text, "authorization")),
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
		if strings.EqualFold(strings.TrimSpace(value), "PLAN") {
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

func ApprovalReviewValueFromText(text string, key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return ""
	}
	lower := strings.ToLower(text)
	needle := key + ":"
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return ""
	}
	valueStart := idx + len(needle)
	value := strings.TrimSpace(text[valueStart:])
	for _, sep := range []string{",", ")"} {
		if cut := strings.Index(value, sep); cut >= 0 {
			value = value[:cut]
		}
	}
	return strings.TrimSpace(value)
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
