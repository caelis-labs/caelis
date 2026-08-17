package controladapter

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func (d *assembler) sessionTokenUsage(ctx context.Context, ref session.SessionRef) (session.UsageSnapshot, error) {
	breakdown, err := d.sessionTokenUsageBreakdown(ctx, ref)
	if err != nil {
		return session.UsageSnapshot{}, err
	}
	return breakdown.Total, nil
}

type sessionTokenUsageBreakdown struct {
	Total      session.UsageSnapshot
	Main       session.UsageSnapshot
	Subagents  session.UsageSnapshot
	AutoReview session.UsageSnapshot
	ByModel    map[string]modelUsageSnapshot
}

type modelUsageSnapshot struct {
	Provider string
	Model    string
	Usage    session.UsageSnapshot
}

func usageSnapshotFromSession(usage session.UsageSnapshot) controlstatus.UsageSnapshot {
	return controlstatus.UsageSnapshot{
		PromptTokens:      usage.PromptTokens,
		CachedInputTokens: usage.CachedInputTokens,
		CompletionTokens:  usage.CompletionTokens,
		ReasoningTokens:   usage.ReasoningTokens,
		TotalTokens:       usage.TotalTokens,
	}
}

const (
	tokenUsageCategoryMain       = "main"
	tokenUsageCategorySubagent   = "subagent"
	tokenUsageCategoryAutoReview = "auto_review"
)

func (d *assembler) sessionTokenUsageBreakdown(ctx context.Context, ref session.SessionRef) (sessionTokenUsageBreakdown, error) {
	if d == nil || d.deps == nil || d.deps.Session.Store == nil {
		return sessionTokenUsageBreakdown{}, nil
	}
	if strings.TrimSpace(ref.SessionID) == "" {
		return sessionTokenUsageBreakdown{}, nil
	}
	events, err := d.deps.Session.Store.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return sessionTokenUsageBreakdown{}, err
	}
	breakdown := sessionTokenUsageBreakdownFromEvents(events, tokenUsageCategoryMain)
	if state, err := d.deps.Session.Store.SnapshotState(ctx, ref); err == nil {
		breakdown.addBreakdown(sessionTokenUsageBreakdownFromState(state))
	}
	for _, childRef := range d.subagentSessionRefs(ctx, ref) {
		childEvents, err := d.deps.Session.Store.Events(ctx, session.EventsRequest{SessionRef: childRef})
		if err != nil {
			continue
		}
		childBreakdown := sessionTokenUsageBreakdownFromEvents(childEvents, tokenUsageCategorySubagent)
		if state, err := d.deps.Session.Store.SnapshotState(ctx, childRef); err == nil {
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
		one := session.UsageSnapshotFromSessionEvent(event)
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
		invocation, hasInvocation := invocationFromSessionEvent(event)
		provider := session.UsageProviderFromSessionEvent(event)
		if provider == "" && hasInvocation {
			provider = invocation.Provider
		}
		usage := session.NormalizeUsageForDisplay(*one, provider)
		breakdown.add(usageCategoryFromSessionEvent(event, fallbackCategory), usage)
		if hasInvocation {
			breakdown.addModel(invocation.Provider, invocation.Model, usage)
		}
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
	autoReviewProvider := anyString(accounting["auto_review_provider"])
	autoReviewModel := anyString(accounting["auto_review_model"])
	autoReviewUsage := session.UsageSnapshotFromMapForProvider(mapAnyValue(accounting[tokenUsageCategoryAutoReview]), autoReviewProvider)
	var autoReviewByModel session.UsageSnapshot
	hasAutoReviewByModel := false
	for _, item := range anySliceValue(accounting["by_model"]) {
		row := mapAnyValue(item)
		if row == nil {
			continue
		}
		if category := normalizeUsageCategory(anyString(row["category"])); category != "" && category != tokenUsageCategoryAutoReview {
			continue
		}
		invocation := session.EventInvocation{Provider: anyString(row["provider"]), Model: anyString(row["model"])}
		usage := session.UsageSnapshotFromMapForProvider(mapAnyValue(row["usage"]), invocation.Provider)
		if usage == nil {
			continue
		}
		normalized := session.NormalizeUsageForDisplay(*usage, invocation.Provider)
		addUsageSnapshot(&autoReviewByModel, normalized)
		hasAutoReviewByModel = true
		breakdown.addModel(invocation.Provider, invocation.Model, normalized)
	}
	if autoReviewUsage != nil {
		usage := session.NormalizeUsageForDisplay(*autoReviewUsage, autoReviewProvider)
		if hasAutoReviewByModel {
			// by_model rows are the authoritative auto-review attribution when
			// present; the aggregate is retained only for older snapshots.
			usage = autoReviewByModel
		} else if autoReviewProvider != "" || autoReviewModel != "" {
			breakdown.addModel(autoReviewProvider, autoReviewModel, usage)
		}
		breakdown.add(tokenUsageCategoryAutoReview, usage)
	}
	return breakdown
}

func (u *sessionTokenUsageBreakdown) add(category string, usage session.UsageSnapshot) {
	if u == nil {
		return
	}
	addUsageSnapshot(&u.Total, usage)
	switch strings.TrimSpace(category) {
	case tokenUsageCategoryAutoReview:
		addUsageSnapshot(&u.AutoReview, usage)
	case tokenUsageCategorySubagent:
		addUsageSnapshot(&u.Subagents, usage)
	default:
		addUsageSnapshot(&u.Main, usage)
	}
}

func (u *sessionTokenUsageBreakdown) addModel(provider string, modelName string, usage session.UsageSnapshot) {
	if u == nil {
		return
	}
	provider, modelName = session.StableInvocationIdentity(provider, modelName)
	if provider == "" && modelName == "" {
		return
	}
	key := provider + "\x00" + modelName
	if u.ByModel == nil {
		u.ByModel = map[string]modelUsageSnapshot{}
	}
	total := u.ByModel[key]
	total.Provider = provider
	total.Model = modelName
	addUsageSnapshot(&total.Usage, usage)
	u.ByModel[key] = total
}

func (u *sessionTokenUsageBreakdown) addBreakdown(other sessionTokenUsageBreakdown) {
	if u == nil {
		return
	}
	addUsageSnapshot(&u.Total, other.Total)
	addUsageSnapshot(&u.Main, other.Main)
	addUsageSnapshot(&u.Subagents, other.Subagents)
	addUsageSnapshot(&u.AutoReview, other.AutoReview)
	for _, item := range other.ByModel {
		u.addModel(item.Provider, item.Model, item.Usage)
	}
}

func addUsageSnapshot(total *session.UsageSnapshot, usage session.UsageSnapshot) {
	if total == nil {
		return
	}
	total.PromptTokens += usage.PromptTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.CompletionTokens += usage.CompletionTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
	if usage.ContextWindowTokens > total.ContextWindowTokens {
		total.ContextWindowTokens = usage.ContextWindowTokens
	}
}

func usageCategoryFromSessionEvent(event *session.Event, fallback string) string {
	if event == nil {
		return firstNonEmpty(fallback, tokenUsageCategoryMain)
	}
	if category := usageCategoryFromMeta(event.Meta); category != "" {
		return category
	}
	if event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantKindSubagent {
		return tokenUsageCategorySubagent
	}
	return firstNonEmpty(fallback, tokenUsageCategoryMain)
}

func invocationFromSessionEvent(event *session.Event) (session.EventInvocation, bool) {
	if event == nil {
		return session.EventInvocation{}, false
	}
	if event.Invocation != nil {
		invocation := session.CloneEventInvocation(*event.Invocation)
		if invocation.Provider != "" || invocation.Model != "" {
			return invocation, true
		}
	}
	for _, meta := range []map[string]any{semanticUsageMetadata(event), event.Meta} {
		if len(meta) == 0 {
			continue
		}
		provider := strings.TrimSpace(anyString(nestedMapAny(meta, "caelis", "invocation", "provider")))
		modelName := strings.TrimSpace(anyString(nestedMapAny(meta, "caelis", "invocation", "model")))
		if provider == "" {
			provider = strings.TrimSpace(anyString(nestedMapAny(meta, "caelis", "sdk", "provider")))
		}
		if modelName == "" {
			modelName = strings.TrimSpace(anyString(nestedMapAny(meta, "caelis", "sdk", "model")))
		}
		if provider == "" {
			provider = strings.TrimSpace(anyString(meta["provider"]))
		}
		if modelName == "" {
			modelName = strings.TrimSpace(anyString(meta["model"]))
		}
		if provider != "" || modelName != "" {
			return session.EventInvocation{Provider: provider, Model: modelName}, true
		}
	}
	return session.EventInvocation{}, false
}

func semanticUsageMetadata(event *session.Event) map[string]any {
	if event == nil {
		return nil
	}
	return event.Meta
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

func anySliceValue(value any) []any {
	switch typed := value.(type) {
	case []any:
		return append([]any(nil), typed...)
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func normalizeUsageCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(category, "-", "_"))) {
	case "auto_review", "autoreview", "review":
		return tokenUsageCategoryAutoReview
	case "subagent", "sub_agent", "child", "child_agent":
		return tokenUsageCategorySubagent
	case "main", "controller":
		return tokenUsageCategoryMain
	default:
		return ""
	}
}

func (d *assembler) subagentSessionRefs(ctx context.Context, ref session.SessionRef) []session.SessionRef {
	if d == nil || d.deps == nil || d.deps.Session.Store == nil {
		return nil
	}
	activeSession, err := d.deps.Session.Store.Session(ctx, ref)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]session.SessionRef, 0, len(activeSession.Participants))
	for _, participant := range activeSession.Participants {
		if participant.Kind != session.ParticipantKindSubagent {
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
		childRef := session.SessionRef{
			AppName:   ref.AppName,
			UserID:    ref.UserID,
			SessionID: sessionID,
		}
		out = append(out, session.NormalizeSessionRef(childRef))
	}
	return out
}

func usageSnapshotDedupeKey(usage session.UsageSnapshot) string {
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.ReasoningTokens == 0 && usage.TotalTokens == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d/%d/%d/%d", usage.PromptTokens, usage.CachedInputTokens, usage.CompletionTokens, usage.ReasoningTokens, usage.TotalTokens)
}

func modelUsageSnapshotsFromBreakdown(breakdown sessionTokenUsageBreakdown) []controlstatus.ModelUsageSnapshot {
	if len(breakdown.ByModel) == 0 {
		return nil
	}
	out := make([]controlstatus.ModelUsageSnapshot, 0, len(breakdown.ByModel))
	for _, item := range breakdown.ByModel {
		out = append(out, controlstatus.ModelUsageSnapshot{
			Provider: item.Provider,
			Model:    item.Model,
			Usage:    usageSnapshotFromSession(item.Usage),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(out[i].Provider + "/" + out[i].Model))
		right := strings.ToLower(strings.TrimSpace(out[j].Provider + "/" + out[j].Model))
		return left < right
	})
	return out
}
