package controladapter

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
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
	events, err := sessionUsageEvents(ctx, d.deps.Session.Store, ref)
	if err != nil {
		return sessionTokenUsageBreakdown{}, err
	}
	if active, err := d.deps.Session.Store.Session(ctx, ref); err == nil {
		events = d.filterCurrentMainContextUsage(ctx, active, events)
	}
	breakdown := sessionTokenUsageBreakdownFromEvents(events, tokenUsageCategoryMain)
	if state, err := d.deps.Session.Store.SnapshotState(ctx, ref); err == nil {
		breakdown.addBreakdown(sessionTokenUsageBreakdownFromState(state))
	}
	aggregatedLocalChildTasks := map[string]struct{}{}
	for _, child := range d.subagentChildren(ctx, ref) {
		childEvents, err := sessionUsageEvents(ctx, d.deps.Session.Store, child.SessionRef)
		if err != nil {
			continue
		}
		if active, err := d.deps.Session.Store.Session(ctx, child.SessionRef); err == nil {
			childEvents = d.filterCurrentMainContextUsage(ctx, active, childEvents)
		}
		childBreakdown := sessionTokenUsageBreakdownFromEvents(childEvents, tokenUsageCategorySubagent)
		if state, err := d.deps.Session.Store.SnapshotState(ctx, child.SessionRef); err == nil {
			childBreakdown.addBreakdown(sessionTokenUsageBreakdownFromState(state))
		}
		breakdown.addBreakdown(childBreakdown)
		if taskID := strings.TrimSpace(child.DelegationID); taskID != "" {
			aggregatedLocalChildTasks[taskID] = struct{}{}
		}
	}
	if d.deps.Status.TaskEntriesFn != nil {
		if entries, err := d.deps.Status.TaskEntriesFn(ctx, ref); err == nil {
			breakdown.addACPTaskUsage(entries, aggregatedLocalChildTasks)
		} else if ctx.Err() != nil {
			return sessionTokenUsageBreakdown{}, ctx.Err()
		}
	}
	return breakdown, nil
}

func (d *assembler) filterCurrentMainContextUsage(ctx context.Context, active session.Session, events []*session.Event) []*session.Event {
	expectedProvider := firstNonEmpty(active.Controller.AgentName, active.Controller.ControllerID, active.Controller.Label)
	expectedModel := strings.TrimSpace(active.Controller.Placement.Model)
	if active.Controller.Kind == session.ControllerKindACP && d != nil && d.deps != nil && d.deps.Agent.ControllerStatusFn != nil {
		if status, found, err := d.deps.Agent.ControllerStatusFn(ctx, active.SessionRef); err == nil && found {
			expectedProvider = firstNonEmpty(status.Agent, expectedProvider)
			expectedModel = firstNonEmpty(status.Model, expectedModel)
		}
	}
	latestMain := latestControllerContextUsageIndex(events, active.Controller, expectedProvider, expectedModel)
	out := make([]*session.Event, 0, len(events))
	for index, event := range events {
		if !isMainContextUsageEvent(event) {
			out = append(out, event)
			continue
		}
		if index == latestMain {
			out = append(out, event)
		}
	}
	return out
}

func isMainContextUsageEvent(event *session.Event) bool {
	return event != nil && event.ContextUsage != nil && (event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "")
}

func latestControllerContextUsageIndex(events []*session.Event, binding session.ControllerBinding, expectedProvider string, expectedModel string) int {
	if binding.Kind != session.ControllerKindACP {
		return -1
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if !isMainContextUsageEvent(event) || !contextUsageBelongsToController(event, binding) {
			continue
		}
		if contextUsageMatchesInvocation(event, expectedProvider, expectedModel) {
			return index
		}
		// A newer gauge from the same controller epoch is a model/configuration
		// boundary. Do not resurrect an older gauge if the controller later
		// returns to the same display model before emitting fresh usage.
		return -1
	}
	return -1
}

func contextUsageBelongsToController(event *session.Event, binding session.ControllerBinding) bool {
	if event == nil || event.ContextUsage == nil || event.Scope == nil || event.Scope.Controller.Kind != session.ControllerKindACP {
		return false
	}
	if expectedEpoch := strings.TrimSpace(binding.EpochID); expectedEpoch != "" && strings.TrimSpace(event.Scope.Controller.EpochID) != expectedEpoch {
		return false
	}
	if expectedID := strings.TrimSpace(binding.ControllerID); expectedID != "" && !strings.EqualFold(strings.TrimSpace(event.Scope.Controller.ID), expectedID) {
		return false
	}
	return true
}

func contextUsageMatchesInvocation(event *session.Event, expectedProvider string, expectedModel string) bool {
	invocation, _ := invocationFromSessionEvent(event)
	if expectedProvider = strings.TrimSpace(expectedProvider); expectedProvider != "" && !strings.EqualFold(invocation.Provider, expectedProvider) {
		return false
	}
	if expectedModel = strings.TrimSpace(expectedModel); expectedModel != "" && !strings.EqualFold(invocation.Model, expectedModel) {
		return false
	}
	return true
}

func sessionUsageEvents(ctx context.Context, reader session.Reader, ref session.SessionRef) ([]*session.Event, error) {
	paged, ok := reader.(session.PagedReader)
	if !ok {
		return reader.Events(ctx, session.EventsRequest{SessionRef: ref})
	}
	var throughSeq uint64
	if checkpointReader, ok := reader.(session.EventCheckpointReader); ok {
		checkpoint, err := checkpointReader.EventCheckpoint(ctx, ref)
		if err != nil {
			return nil, err
		}
		throughSeq = checkpoint.ThroughSeq
	}
	var events []*session.Event
	var afterSeq uint64
	for {
		page, err := paged.EventsPage(ctx, session.EventPageRequest{
			SessionRef: ref,
			AfterSeq:   afterSeq,
			ThroughSeq: throughSeq,
			Visibility: session.EventPageClientReplay,
		})
		if err != nil {
			return nil, err
		}
		events = append(events, page.Events...)
		if !page.HasMore {
			return events, nil
		}
		if page.NextSeq <= afterSeq {
			return nil, fmt.Errorf("read session usage events: page cursor did not advance past %d", afterSeq)
		}
		afterSeq = page.NextSeq
	}
}

func sessionTokenUsageBreakdownFromEvents(events []*session.Event, fallbackCategory string) sessionTokenUsageBreakdown {
	var breakdown sessionTokenUsageBreakdown
	latestContextUsage := map[string]modelUsageSnapshot{}
	latestContextCategory := map[string]string{}
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
		if event.ContextUsage != nil {
			invocation, _ := invocationFromSessionEvent(event)
			lane := contextUsageLaneKey(event, fallbackCategory)
			latestContextUsage[lane] = modelUsageSnapshot{
				Provider: invocation.Provider,
				Model:    invocation.Model,
				Usage:    *one,
			}
			latestContextCategory[lane] = usageCategoryFromSessionEvent(event, fallbackCategory)
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
	for lane, item := range latestContextUsage {
		usage := session.NormalizeUsageForDisplay(item.Usage, item.Provider)
		breakdown.add(latestContextCategory[lane], usage)
		breakdown.addModel(item.Provider, item.Model, usage)
	}
	return breakdown
}

func contextUsageLaneKey(event *session.Event, fallbackCategory string) string {
	category := usageCategoryFromSessionEvent(event, fallbackCategory)
	if event == nil || event.Scope == nil {
		return category + "\x00main"
	}
	participantID := strings.TrimSpace(event.Scope.Participant.ID)
	if participantID == "" {
		return category + "\x00main"
	}
	return category + "\x00" + participantID
}

// addACPTaskUsage adds replaceable ACP Task gauges for subagent Tasks that do
// not already have a readable local child Session aggregated from a parent
// subagent participant. Local child Session usage is authoritative; Task gauges
// remain the fallback for remote ACP Sessions without a local durable mirror.
func (u *sessionTokenUsageBreakdown) addACPTaskUsage(entries []*taskapi.Entry, aggregatedLocalChildTasks map[string]struct{}) {
	if u == nil {
		return
	}
	for _, entry := range entries {
		if entry == nil || entry.Kind != taskapi.KindSubagent || entry.ContextUsage == nil {
			continue
		}
		if _, ok := aggregatedLocalChildTasks[strings.TrimSpace(entry.TaskID)]; ok {
			continue
		}
		usage := session.UsageSnapshotFromContextUsage(entry.ContextUsage.Snapshot)
		if usage == nil {
			continue
		}
		invocation := session.CloneEventInvocation(entry.ContextUsage.Invocation)
		normalized := session.NormalizeUsageForDisplay(*usage, invocation.Provider)
		u.add(tokenUsageCategorySubagent, normalized)
		u.addModel(invocation.Provider, invocation.Model, normalized)
	}
}

func (d *assembler) latestACPControllerContextUsage(
	ctx context.Context,
	active session.Session,
	expectedProvider string,
	expectedModel string,
) (session.UsageSnapshot, bool, error) {
	if d == nil || d.deps == nil || d.deps.Session.Store == nil {
		return session.UsageSnapshot{}, false, nil
	}
	events, err := sessionUsageEvents(ctx, d.deps.Session.Store, active.SessionRef)
	if err != nil {
		return session.UsageSnapshot{}, false, err
	}
	index := latestControllerContextUsageIndex(events, active.Controller, expectedProvider, expectedModel)
	if index < 0 {
		return session.UsageSnapshot{}, false, nil
	}
	usage := session.UsageSnapshotFromContextUsage(*events[index].ContextUsage)
	if usage == nil {
		return session.UsageSnapshot{}, false, nil
	}
	return *usage, true, nil
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
	if event.Scope != nil && (event.Scope.Participant.Kind == session.ParticipantKindSubagent || event.Scope.Participant.Role == session.ParticipantRoleDelegated) {
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

type subagentChildRef struct {
	SessionRef   session.SessionRef
	DelegationID string
}

func (d *assembler) subagentChildren(ctx context.Context, ref session.SessionRef) []subagentChildRef {
	if d == nil || d.deps == nil || d.deps.Session.Store == nil {
		return nil
	}
	activeSession, err := d.deps.Session.Store.Session(ctx, ref)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]subagentChildRef, 0, len(activeSession.Participants))
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
		out = append(out, subagentChildRef{
			SessionRef:   session.NormalizeSessionRef(childRef),
			DelegationID: strings.TrimSpace(participant.DelegationID),
		})
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
