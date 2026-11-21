package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// CompactionConfig controls codex-style checkpoint compaction.
type CompactionConfig struct {
	// Async opts into turn-scoped speculative compaction. The zero value is off.
	Async AsyncCompactionConfig
	// Idle opts into Host-coordinated compaction after successful Turn completion.
	Idle                        IdleCompactionConfig
	Enabled                     bool
	WatermarkRatio              float64
	ForceWatermarkRatio         float64
	DefaultContextWindowTokens  int
	ReserveOutputTokens         int
	SafetyMarginTokens          int
	SegmentTokenBudget          int
	MaxSegmentDepth             int
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
	if cfg.EstimatedPromptPrefixTokens < 0 {
		cfg.EstimatedPromptPrefixTokens = 0
	}
	return cfg
}

type codexStyleCompactor struct {
	cfg         CompactionConfig
	diagnostics *slog.Logger
}

func newCodexStyleCompactor(cfg CompactionConfig, diagnostics *slog.Logger) compact.Engine {
	return &codexStyleCompactor{cfg: normalizeCompactionConfig(cfg), diagnostics: diagnostics}
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
	decision := c.decide(result.Usage, req)
	if !decision.ShouldCompact {
		return result, nil
	}
	compacted, err := c.compact(ctx, req, decision.Reason)
	if err != nil {
		return result, err
	}
	return compacted, nil
}

func (c *codexStyleCompactor) Force(ctx context.Context, req compact.Request, trigger string) (compact.Result, error) {
	if err := req.ValidateCoverage(); err != nil {
		return compact.Result{}, err
	}
	promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
	result := compact.Result{
		PromptEvents: promptEvents,
		Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
	}
	if len(compact.EventsAfterLatestCompact(req.Events)) == 0 {
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
	if !model.IsContextOverflow(cause) {
		return compact.Result{}, cause
	}
	return c.compact(ctx, req, "overflow_recovery")
}

func (c *codexStyleCompactor) decide(usage compact.UsageSnapshot, req compact.Request) compact.TriggerDecision {
	if usage.EffectiveInputBudget <= 0 || req.Model == nil {
		return compact.TriggerDecision{}
	}
	if len(compact.EventsAfterLatestCompact(req.Events)) == 0 {
		return compact.TriggerDecision{}
	}
	return evaluateWatermark(usage, c.cfg)
}

func (c *codexStyleCompactor) compact(ctx context.Context, req compact.Request, trigger string) (compact.Result, error) {
	if err := req.ValidateCoverage(); err != nil {
		return compact.Result{}, err
	}
	baseEvent, baseData, _ := compact.LatestCompactEvent(req.Events)
	baseText := stripRuntimeContinuity(eventTextForCompaction(baseEvent))
	delta := compact.EventsAfterLatestCompact(req.Events)
	if len(delta) == 0 {
		promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
		return compact.Result{
			PromptEvents: promptEvents,
			Usage:        c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents)),
		}, nil
	}

	notifyCompactionStarted(ctx)
	generator := "model_markdown"
	compactText := ""
	var err error
	if req.InContextRequest != nil {
		compactText, err = modelCompactMarkdownInContext(ctx, req.Model, req.InContextRequest)
		if err == nil {
			generator = "model_context"
		} else if ctx.Err() != nil {
			return compact.Result{}, ctx.Err()
		} else if req.RequireInContext || (req.Coverage != nil && req.Coverage.RequireInContext) {
			return compact.Result{}, err
		} else if isCompactionTerminalError(err) {
			return compact.Result{}, err
		} else if model.IsRetryableLLMError(err) {
			// Provider recovery belongs to the existing request retry owner. A
			// returned failure must not restart its budget via another prompt.
			return compact.Result{}, err
		} else {
			c.logFallback(ctx, "in_context", "independent", classifyCompactionFailure(err))
		}
	}
	if compactText == "" {
		compactText, err = c.generateCompactMarkdown(ctx, req.Model, baseText, delta, req.ServiceTier)
		if err != nil {
			if ctx.Err() != nil {
				return compact.Result{}, ctx.Err()
			}
			return compact.Result{}, err
		}
	}
	compactText = appendRuntimeContinuity(compactText, req.RuntimeAppendix)
	if strings.TrimSpace(compactText) == "" {
		return compact.Result{}, errors.New("agent-sdk/runtime: compaction produced no checkpoint")
	}
	data := session.CheckpointData{
		Revision:             baseData.Revision + 1,
		ContractVersion:      compact.CompactContractVersion,
		SummarizedThroughID:  lastEventID(delta),
		SummarizedThroughSeq: session.LastEventSeq(delta),
		Generator:            generator,
		Trigger:              strings.TrimSpace(trigger),
		SourceEventCount:     len(delta),
	}
	compactEvent := buildCompactEvent(req.Session, compactText, data)
	promptEvents := compact.PromptEventsFromLatestCompact([]*session.Event{compactEvent})
	usage := c.snapshotUsage(req, promptEventsWithPending(promptEvents, req.PendingEvents))
	data.TotalTokens = usage.TotalTokens
	data.ContextWindowTokens = usage.ContextWindowTokens
	if compactEvent.Meta == nil {
		compactEvent.Meta = map[string]any{}
	}
	compactEvent.Meta[session.CheckpointMetadataKey] = session.CheckpointDataValue(data)
	return compact.Result{
		Compacted:    true,
		CompactText:  compactText,
		CompactEvent: compactEvent,
		PromptEvents: promptEvents,
		Usage:        usage,
	}, nil
}

func (c *codexStyleCompactor) logFallback(ctx context.Context, from, to, reason string) {
	if c == nil || c.diagnostics == nil {
		return
	}
	c.diagnostics.WarnContext(ctx, "context compaction fallback",
		slog.String("component", "context_compaction"),
		slog.String("from", strings.TrimSpace(from)),
		slog.String("to", strings.TrimSpace(to)),
		slog.String("reason", strings.TrimSpace(reason)),
	)
}

func classifyCompactionFailure(err error) string {
	switch {
	case err == nil:
		return "unknown"
	case model.IsContextOverflow(err):
		return "context_overflow"
	case errors.Is(err, errCompactionToolRequest):
		return "tool_request"
	case errors.Is(err, errCompactionUnusableOutput):
		return "unusable_output"
	default:
		return "model_request"
	}
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

func computeUsageSnapshotForModel(
	events []*session.Event,
	contextWindow int,
	cfg CompactionConfig,
	provider, modelName string,
	estimate func(*session.Event) int,
) compact.UsageSnapshot {
	promptEvents := compact.PromptEventsFromLatestCompact(events)
	// When a provider omits usage, prefer the prefix recorded from the actual
	// last model-visible request over the static assembly fallback. This reuses
	// the request-boundary fingerprint path and avoids reconstructing ToolSearch
	// visibility or dynamic Spawn specs in the status service.
	if tokens, ok := latestPromptPrefixTokensForIdentity(events, provider, modelName); ok {
		cfg.EstimatedPromptPrefixTokens = tokens
	}
	usage, _, _ := snapshotUsageWithResolvedWindowDetailsUsing(
		promptEvents,
		contextWindow,
		cfg,
		func(snapshot providerTokenSnapshot) bool {
			return providerSnapshotCompatibleWithIdentity(snapshot, provider, modelName)
		},
		estimate,
	)
	return usage
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
