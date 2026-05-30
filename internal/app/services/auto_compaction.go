package services

import (
	"context"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
)

const defaultAutoCompactionWatermarkRatio = 0.8

type AutoCompactionPolicy struct {
	Enabled        bool    `json:"enabled"`
	WatermarkRatio float64 `json:"watermark_ratio,omitempty"`
	Source         string  `json:"source,omitempty"`
}

func (s CompactionService) AutoPolicy(context.Context) (AutoCompactionPolicy, error) {
	settingsPolicy := appsettings.CompactionPolicy{}
	if s.services.settings != nil {
		settingsPolicy = s.services.settings.CompactionPolicy()
	}
	return autoCompactionPolicy(settingsPolicy), nil
}

func (s TurnService) autoCompactBeforeTurn(ctx context.Context, ref session.Ref, req BeginTurnRequest, modelRef string) ([]session.Event, error) {
	policy, err := s.services.Compaction().AutoPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled || policy.WatermarkRatio <= 0 || strings.TrimSpace(ref.SessionID) == "" {
		return nil, nil
	}
	snapshot, err := s.services.Sessions().Load(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !hasAutoCompactionDelta(snapshot.Events) {
		return nil, nil
	}
	budget, err := s.services.contextBudgetFromSnapshotWithModel(ctx, snapshot, modelRef)
	if err != nil {
		return nil, err
	}
	if budget.EffectiveInputBudget <= 0 {
		return nil, nil
	}
	estimatedInput := budget.EstimatedInputTokens + estimatePendingTurnTokens(req.Input, req.ContentParts)
	if estimatedInput <= 0 {
		return nil, nil
	}
	ratio := float64(estimatedInput) / float64(budget.EffectiveInputBudget)
	if ratio < policy.WatermarkRatio {
		return nil, nil
	}
	event, err := s.services.Compaction().Compact(ctx, CompactSessionRequest{
		SessionRef: ref,
		Trigger:    "context_watermark",
	})
	if err != nil {
		return nil, err
	}
	return []session.Event{event}, nil
}

func autoCompactionPolicy(settingsPolicy appsettings.CompactionPolicy) AutoCompactionPolicy {
	settingsPolicy = appsettings.NormalizeCompactionPolicy(settingsPolicy)
	mode := strings.ToLower(strings.TrimSpace(settingsPolicy.Auto.Mode))
	enabled := mode != "disabled"
	source := "default"
	if mode != "" || settingsPolicy.Auto.WatermarkRatio > 0 {
		source = "settings"
	}
	ratio := settingsPolicy.Auto.WatermarkRatio
	if ratio <= 0 || ratio > 1 {
		ratio = defaultAutoCompactionWatermarkRatio
	}
	return AutoCompactionPolicy{
		Enabled:        enabled,
		WatermarkRatio: ratio,
		Source:         source,
	}
}

func hasAutoCompactionDelta(events []session.Event) bool {
	if len(events) == 0 {
		return false
	}
	start := 0
	for i := len(events) - 1; i >= 0; i-- {
		if session.IsTransient(events[i]) {
			continue
		}
		if isCompactCheckpoint(events[i]) {
			start = i + 1
			break
		}
	}
	for _, event := range events[start:] {
		if session.IsTransient(event) || event.Message == nil {
			continue
		}
		if strings.TrimSpace(event.Message.TextContent()) != "" || len(event.Message.ToolCalls()) > 0 || len(event.Message.Parts) > 0 {
			return true
		}
	}
	return false
}

func estimatePendingTurnTokens(input string, parts []model.ContentPart) int {
	total := estimateContextTextTokens(string(model.RoleUser)) + 4
	usedParts := false
	for _, part := range parts {
		switch part.Type {
		case model.ContentPartText:
			total += estimateContextTextTokens(part.Text)
			usedParts = true
		case model.ContentPartImage:
			total += 32 +
				estimateContextTextTokens(part.MimeType) +
				estimateContextTextTokens(part.URI) +
				estimateContextTextTokens(part.Data) +
				estimateContextTextTokens(part.FileName)
			usedParts = true
		case model.ContentPartFile:
			total += 16 +
				estimateContextTextTokens(part.MimeType) +
				estimateContextTextTokens(part.URI) +
				estimateContextTextTokens(part.Data) +
				estimateContextTextTokens(part.FileName)
			usedParts = true
		}
	}
	if !usedParts {
		total += estimateContextTextTokens(input)
	}
	return total
}

func turnWithPrefixedEvents(base coreruntime.Turn, prefix []session.Event) coreruntime.Turn {
	if base == nil || len(prefix) == 0 {
		return base
	}
	out := make(chan coreruntime.EventEnvelope, len(prefix)+32)
	wrapped := &prefixedTurn{
		Turn:   base,
		events: out,
	}
	go func() {
		defer close(out)
		for _, event := range prefix {
			out <- coreruntime.EventEnvelope{Event: session.CloneEvent(event)}
		}
		for env := range base.Events() {
			out <- env
		}
	}()
	return wrapped
}

type prefixedTurn struct {
	coreruntime.Turn
	events <-chan coreruntime.EventEnvelope
}

func (t *prefixedTurn) Events() <-chan coreruntime.EventEnvelope {
	return t.events
}

func (t *prefixedTurn) StartedAt() time.Time {
	return t.Turn.StartedAt()
}
