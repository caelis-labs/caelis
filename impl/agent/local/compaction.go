package local

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/session"
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
		return compact.Result{}, errors.New("impl/agent/local: compact model is required")
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

	compactText, err := c.generateCompactMarkdown(ctx, req.Model, baseText, summaryEvents)
	if err != nil {
		return compact.Result{}, err
	}
	data := compact.CompactEventData{
		Revision:            baseData.Revision + 1,
		ContractVersion:     compact.CompactContractVersion,
		SummarizedThroughID: lastEventID(delta),
		Generator:           "model_markdown",
		Trigger:             strings.TrimSpace(trigger),
		SourceEventCount:    len(summaryEvents),
		DiscoveredTools:     discoveredToolNamesFromEvents(req.Events),
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

func (c *codexStyleCompactor) snapshotUsage(req compact.Request, promptEvents []*session.Event) compact.UsageSnapshot {
	window := resolveContextWindowTokens(req.Model, c.cfg.DefaultContextWindowTokens)
	return snapshotUsageWithResolvedWindow(promptEvents, window, c.cfg)
}

// ComputeUsageSnapshot applies the same provider-aware usage snapshot logic
// used by compaction, but without mutating session history.
func ComputeUsageSnapshot(events []*session.Event, pendingEvents []*session.Event, contextWindow int, cfg CompactionConfig) compact.UsageSnapshot {
	promptEvents := compact.PromptEventsFromLatestCompact(events)
	return snapshotUsageWithResolvedWindow(promptEventsWithPending(promptEvents, pendingEvents), contextWindow, cfg)
}
