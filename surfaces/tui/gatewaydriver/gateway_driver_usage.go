package gatewaydriver

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (d *GatewayDriver) sessionTokenUsage(ctx context.Context, ref session.SessionRef) (kernel.UsageSnapshot, error) {
	breakdown, err := d.sessionTokenUsageBreakdown(ctx, ref)
	if err != nil {
		return kernel.UsageSnapshot{}, err
	}
	return breakdown.Total, nil
}

type sessionTokenUsageBreakdown struct {
	Total      kernel.UsageSnapshot
	Main       kernel.UsageSnapshot
	Subagents  kernel.UsageSnapshot
	AutoReview kernel.UsageSnapshot
	Compaction kernel.UsageSnapshot
}

const (
	tokenUsageCategoryMain       = "main"
	tokenUsageCategorySubagent   = "subagent"
	tokenUsageCategoryAutoReview = "auto_review"
	tokenUsageCategoryCompaction = "compact"
)

func (d *GatewayDriver) sessionTokenUsageBreakdown(ctx context.Context, ref session.SessionRef) (sessionTokenUsageBreakdown, error) {
	if d == nil || d.stack == nil || d.stack.Sessions == nil {
		return sessionTokenUsageBreakdown{}, nil
	}
	if strings.TrimSpace(ref.SessionID) == "" {
		return sessionTokenUsageBreakdown{}, nil
	}
	events, err := d.stack.Sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return sessionTokenUsageBreakdown{}, err
	}
	breakdown := sessionTokenUsageBreakdownFromEvents(events, tokenUsageCategoryMain)
	if state, err := d.stack.Sessions.SnapshotState(ctx, ref); err == nil {
		breakdown.addBreakdown(sessionTokenUsageBreakdownFromState(state))
	}
	for _, childRef := range d.selfSubagentSessionRefs(ctx, ref) {
		childEvents, err := d.stack.Sessions.Events(ctx, session.EventsRequest{SessionRef: childRef})
		if err != nil {
			continue
		}
		childBreakdown := sessionTokenUsageBreakdownFromEvents(childEvents, tokenUsageCategorySubagent)
		if state, err := d.stack.Sessions.SnapshotState(ctx, childRef); err == nil {
			childBreakdown.addBreakdown(sessionTokenUsageBreakdownFromState(state))
		}
		breakdown.addBreakdown(childBreakdown)
	}
	return breakdown, nil
}

func sessionTokenUsageBreakdownFromEvents(events []*session.Event, fallbackCategory string) sessionTokenUsageBreakdown {
	var breakdown sessionTokenUsageBreakdown
	lastToolCallUsageKey := ""
	lastUsageWasToolCall := false
	for _, event := range events {
		one := kernel.UsageSnapshotFromSessionEvent(event)
		if one == nil {
			if session.EventTypeOf(event) != session.EventTypeToolCall {
				lastToolCallUsageKey = ""
				lastUsageWasToolCall = false
			}
			continue
		}
		isToolCall := session.EventTypeOf(event) == session.EventTypeToolCall
		usageKey := usageSnapshotDedupeKey(*one)
		if isToolCall && lastUsageWasToolCall && usageKey != "" && usageKey == lastToolCallUsageKey {
			continue
		}
		breakdown.add(usageCategoryFromSessionEvent(event, fallbackCategory), *one)
		if isToolCall {
			lastToolCallUsageKey = usageKey
			lastUsageWasToolCall = true
		} else {
			lastToolCallUsageKey = ""
			lastUsageWasToolCall = false
		}
	}
	return breakdown
}

func sessionTokenUsageBreakdownFromState(state map[string]any) sessionTokenUsageBreakdown {
	var breakdown sessionTokenUsageBreakdown
	accounting := mapAnyValue(state[kernel.StateUsageAccounting])
	if usage := kernel.UsageSnapshotFromMap(mapAnyValue(accounting[tokenUsageCategoryAutoReview])); usage != nil {
		breakdown.add(tokenUsageCategoryAutoReview, *usage)
	}
	return breakdown
}

func (u *sessionTokenUsageBreakdown) add(category string, usage kernel.UsageSnapshot) {
	if u == nil {
		return
	}
	addUsageSnapshot(&u.Total, usage)
	switch strings.TrimSpace(category) {
	case tokenUsageCategoryAutoReview:
		addUsageSnapshot(&u.AutoReview, usage)
	case tokenUsageCategorySubagent:
		addUsageSnapshot(&u.Subagents, usage)
	case tokenUsageCategoryCompaction:
		addUsageSnapshot(&u.Compaction, usage)
	default:
		addUsageSnapshot(&u.Main, usage)
	}
}

func (u *sessionTokenUsageBreakdown) addBreakdown(other sessionTokenUsageBreakdown) {
	if u == nil {
		return
	}
	addUsageSnapshot(&u.Total, other.Total)
	addUsageSnapshot(&u.Main, other.Main)
	addUsageSnapshot(&u.Subagents, other.Subagents)
	addUsageSnapshot(&u.AutoReview, other.AutoReview)
	addUsageSnapshot(&u.Compaction, other.Compaction)
}

func addUsageSnapshot(total *kernel.UsageSnapshot, usage kernel.UsageSnapshot) {
	if total == nil {
		return
	}
	total.PromptTokens += usage.PromptTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.CompletionTokens += usage.CompletionTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
}

func usageCategoryFromSessionEvent(event *session.Event, fallback string) string {
	if event == nil {
		return firstNonEmpty(fallback, tokenUsageCategoryMain)
	}
	if category := usageCategoryFromMeta(event.Meta); category != "" {
		return category
	}
	if session.EventTypeOf(event) == session.EventTypeCompact {
		return tokenUsageCategoryCompaction
	}
	if event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantKindSubagent {
		return tokenUsageCategorySubagent
	}
	return firstNonEmpty(fallback, tokenUsageCategoryMain)
}

func usageCategoryFromMeta(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	for _, key := range []string{"usage_category", "usageCategory", "category"} {
		if category := normalizeUsageCategory(anyString(meta[key])); category != "" {
			return category
		}
	}
	if category := normalizeUsageCategory(anyString(nestedMapAny(meta, "caelis", "usage", "category"))); category != "" {
		return category
	}
	if category := normalizeUsageCategory(anyString(nestedMapAny(meta, "caelis", "sdk", "usage_category"))); category != "" {
		return category
	}
	if strings.EqualFold(anyString(meta["decision_source"]), "auto-review") ||
		strings.EqualFold(anyString(meta["source"]), "auto_review") {
		return tokenUsageCategoryAutoReview
	}
	return ""
}

func nestedMapAny(values map[string]any, path ...string) any {
	if len(values) == 0 {
		return nil
	}
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}

func mapAnyValue(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return maps.Clone(typed)
	}
	return nil
}

func normalizeUsageCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(category, "-", "_"))) {
	case "auto_review", "autoreview", "review":
		return tokenUsageCategoryAutoReview
	case "subagent", "sub_agent", "child", "child_agent":
		return tokenUsageCategorySubagent
	case "compact", "compaction":
		return tokenUsageCategoryCompaction
	case "main", "controller":
		return tokenUsageCategoryMain
	default:
		return ""
	}
}

func (d *GatewayDriver) selfSubagentSessionRefs(ctx context.Context, ref session.SessionRef) []session.SessionRef {
	if d == nil || d.stack == nil || d.stack.Sessions == nil {
		return nil
	}
	activeSession, err := d.stack.Sessions.Session(ctx, ref)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]session.SessionRef, 0, len(activeSession.Participants))
	for _, participant := range activeSession.Participants {
		if participant.Kind != session.ParticipantKindSubagent {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(participant.AgentName), "self") {
			continue
		}
		sessionID := strings.TrimSpace(participant.SessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		childRef := ref
		childRef.SessionID = sessionID
		out = append(out, session.NormalizeSessionRef(childRef))
	}
	return out
}

func usageSnapshotDedupeKey(usage kernel.UsageSnapshot) string {
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.ReasoningTokens == 0 && usage.TotalTokens == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d/%d/%d/%d", usage.PromptTokens, usage.CachedInputTokens, usage.CompletionTokens, usage.ReasoningTokens, usage.TotalTokens)
}
