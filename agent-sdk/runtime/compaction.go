package runtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// CompactionConfig controls codex-style checkpoint compaction.
type CompactionConfig struct {
	Enabled                     bool
	WatermarkRatio              float64
	ForceWatermarkRatio         float64
	DefaultContextWindowTokens  int
	ReserveOutputTokens         int
	SafetyMarginTokens          int
	SegmentTokenBudget          int
	MaxSegmentDepth             int
	MaxRetryAttempts            int
	RetryBaseDelay              time.Duration
	RetryMaxDelay               time.Duration
	EstimatedPromptPrefixTokens int
}

func normalizeCompactionConfig(cfg CompactionConfig) CompactionConfig {
	if cfg.DefaultContextWindowTokens <= 0 {
		cfg.DefaultContextWindowTokens = 200000
	}
	if cfg.SegmentTokenBudget <= 0 {
		cfg.SegmentTokenBudget = 24000
	}
	if cfg.MaxSegmentDepth <= 0 {
		cfg.MaxSegmentDepth = 8
	}
	if cfg.MaxRetryAttempts <= 0 {
		cfg.MaxRetryAttempts = 3
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = 500 * time.Millisecond
	}
	if cfg.RetryMaxDelay <= 0 {
		cfg.RetryMaxDelay = 8 * time.Second
	}
	if cfg.EstimatedPromptPrefixTokens < 0 {
		cfg.EstimatedPromptPrefixTokens = 0
	}
	return cfg
}

type codexStyleCompactor struct {
	cfg CompactionConfig
}

func newCodexStyleCompactor(cfg CompactionConfig) compact.Engine {
	return &codexStyleCompactor{cfg: normalizeCompactionConfig(cfg)}
}

func (c *codexStyleCompactor) Prepare(ctx context.Context, req compact.Request) (compact.Result, error) {
	promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
	usagePromptEvents := promptEventsWithPending(promptEvents, req.PendingEvents)
	result := compact.Result{
		PromptEvents: promptEvents,
		Usage:        c.snapshotUsage(req, usagePromptEvents),
	}
	if !c.cfg.Enabled || req.Model == nil {
		return result, nil
	}
	decision, err := c.decide(ctx, result.Usage, req)
	if err != nil || !decision.ShouldCompact {
		return result, err
	}
	compacted, err := c.compact(ctx, req, decision.Reason)
	if err != nil {
		return result, err
	}
	return compacted, nil
}

func (c *codexStyleCompactor) Force(ctx context.Context, req compact.Request, trigger string) (compact.Result, error) {
	promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
	result := compact.Result{
		PromptEvents: promptEvents,
		Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
	}
	if compactableEventCount(req.Events) == 0 {
		return result, nil
	}
	if req.Model == nil {
		return compact.Result{}, errors.New("agent-sdk/runtime: compact model is required")
	}
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		trigger = "manual"
	}
	return c.compact(ctx, req, trigger)
}

func (c *codexStyleCompactor) CompactOnOverflow(ctx context.Context, req compact.Request, cause error) (compact.Result, error) {
	if !c.cfg.Enabled || req.Model == nil {
		promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
		return compact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, cause
	}
	if !isCompactionOverflowError(cause) {
		return compact.Result{}, cause
	}
	return c.compact(ctx, req, "overflow_recovery")
}

func (c *codexStyleCompactor) decide(_ context.Context, usage compact.UsageSnapshot, req compact.Request) (compact.TriggerDecision, error) {
	if usage.EffectiveInputBudget <= 0 || req.Model == nil {
		return compact.TriggerDecision{}, nil
	}
	if compactableEventCount(req.Events) == 0 {
		return compact.TriggerDecision{}, nil
	}
	return evaluateWatermark(usage, c.cfg), nil
}

func (c *codexStyleCompactor) compact(ctx context.Context, req compact.Request, trigger string) (compact.Result, error) {
	baseEvent, baseData, _ := compact.LatestCompactEvent(req.Events)
	baseText := compactTextFromEvent(baseEvent)
	delta := compactableEvents(req.Events)
	if len(delta) == 0 {
		promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
		return compact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, nil
	}
	summaryEvents := session.CloneEvents(delta)
	if len(summaryEvents) == 0 {
		promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
		return compact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, nil
	}

	notifyCompactionStarted(ctx)
	compactText, err := c.generateCompactMarkdown(ctx, req.Model, baseText, summaryEvents)
	if err != nil {
		return compact.Result{}, err
	}
	data := compact.CompactEventData{
		Revision:             baseData.Revision + 1,
		ContractVersion:      compact.CompactContractVersion,
		SummarizedThroughID:  lastEventID(delta),
		SummarizedThroughSeq: lastEventSeq(delta),
		Generator:            "model_markdown",
		Trigger:              strings.TrimSpace(trigger),
		SourceEventCount:     len(summaryEvents),
		DiscoveredTools:      discoveredToolNamesFromEvents(req.Events),
	}
	compactEvent := buildCompactEvent(req.Session, compactText, data)
	promptEvents := compact.PromptEventsFromLatestCompact([]*session.Event{compactEvent})
	usage := c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents))
	data.TotalTokens = usage.TotalTokens
	data.ContextWindowTokens = usage.ContextWindowTokens
	if compactEvent.Meta == nil {
		compactEvent.Meta = map[string]any{}
	}
	compactEvent.Meta[compact.MetaKeyCompact] = compact.CompactEventDataValue(data)
	return compact.Result{
		Compacted:    true,
		CompactText:  compactText,
		CompactEvent: compactEvent,
		PromptEvents: promptEvents,
		Usage:        usage,
	}, nil
}

func lastEventSeq(events []*session.Event) uint64 {
	return session.LastEventSeq(events)
}

func (c *codexStyleCompactor) snapshotUsage(req compact.Request, promptEvents []*session.Event) compact.UsageSnapshot {
	window := resolveContextWindowTokens(req.Model, c.cfg.DefaultContextWindowTokens)
	if req.Model == nil {
		return snapshotUsageWithResolvedWindow(promptEvents, window, c.cfg)
	}
	return snapshotUsageWithResolvedWindowUsing(promptEvents, window, c.cfg, func(snapshot providerTokenSnapshot) bool {
		return providerSnapshotCompatibleWithLLM(snapshot, req.Model)
	})
}

// ComputeUsageSnapshot projects the latest provider-aware usage for reporting
// without mutating Session history. Callers that know the active model identity
// should use ComputeUsageSnapshotForModel so another model cannot seed the
// active context meter.
func ComputeUsageSnapshot(events []*session.Event, pendingEvents []*session.Event, contextWindow int, cfg CompactionConfig) compact.UsageSnapshot {
	promptEvents := compact.PromptEventsFromLatestCompact(events)
	return snapshotUsageWithResolvedWindow(promptEventsWithPending(promptEvents, pendingEvents), contextWindow, cfg)
}

// ComputeUsageSnapshotForModel projects reporting usage using only a provider
// baseline produced by the named model. Incomplete or mismatched identities
// fail closed to the local prompt estimate.
func ComputeUsageSnapshotForModel(
	events []*session.Event,
	pendingEvents []*session.Event,
	contextWindow int,
	cfg CompactionConfig,
	provider string,
	modelName string,
) compact.UsageSnapshot {
	promptEvents := compact.PromptEventsFromLatestCompact(events)
	promptEvents = promptEventsWithPending(promptEvents, pendingEvents)
	// When a provider omits usage, prefer the prefix recorded from the actual
	// last model-visible request over the static assembly fallback. This reuses
	// the request-boundary fingerprint path and avoids reconstructing ToolSearch
	// visibility or dynamic Spawn specs in the status service.
	if tokens, ok := latestPromptPrefixTokensForIdentity(events, provider, modelName); ok {
		cfg.EstimatedPromptPrefixTokens = tokens
	}
	return snapshotUsageWithResolvedWindowUsing(
		promptEvents,
		contextWindow,
		cfg,
		func(snapshot providerTokenSnapshot) bool {
			return providerSnapshotCompatibleWithIdentity(snapshot, provider, modelName)
		},
	)
}

func latestPromptPrefixTokensForIdentity(events []*session.Event, provider string, modelName string) (int, bool) {
	provider, modelName = session.StableInvocationIdentity(provider, modelName)
	if provider == "" || modelName == "" {
		return 0, false
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event == nil || event.Invocation == nil || event.Invocation.PromptPrefixTokens <= 0 {
			continue
		}
		candidateProvider, candidateModel := session.StableInvocationIdentity(
			event.Invocation.Provider,
			event.Invocation.Model,
		)
		if strings.EqualFold(candidateProvider, provider) && strings.EqualFold(candidateModel, modelName) {
			return event.Invocation.PromptPrefixTokens, true
		}
	}
	return 0, false
}
