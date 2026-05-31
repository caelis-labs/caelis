package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	"github.com/OnslaughtSnail/caelis/core/session"
	appprompt "github.com/OnslaughtSnail/caelis/internal/app/prompt"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	enginecontext "github.com/OnslaughtSnail/caelis/internal/engine/context"
)

const contextBudgetSourceEstimated = "estimated"

type ContextBudgetRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
}

func (s CompactionService) ContextBudget(ctx context.Context, req ContextBudgetRequest) (appviewmodel.ContextBudget, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.services.engine == nil {
		return appviewmodel.ContextBudget{}, errors.New("app/services: runtime engine is required")
	}
	ref := defaultSessionRef(s.services.Runtime(), req.SessionRef)
	if strings.TrimSpace(ref.SessionID) == "" {
		return appviewmodel.ContextBudget{}, fmt.Errorf("%w: session id is required", session.ErrInvalid)
	}
	snapshot, err := s.services.Sessions().Load(ctx, ref)
	if err != nil {
		return appviewmodel.ContextBudget{}, err
	}
	return s.services.contextBudgetFromSnapshot(ctx, snapshot)
}

func (s StatusService) contextBudget(ctx context.Context, snapshot session.Snapshot) (appviewmodel.ContextBudget, error) {
	return s.services.contextBudgetFromSnapshot(ctx, snapshot)
}

func (s Services) contextBudgetFromSnapshot(ctx context.Context, snapshot session.Snapshot) (appviewmodel.ContextBudget, error) {
	return s.contextBudgetFromSnapshotWithModel(ctx, snapshot, "")
}

func (s Services) contextBudgetFromSnapshotWithModel(ctx context.Context, snapshot session.Snapshot, modelRef string) (appviewmodel.ContextBudget, error) {
	messages := enginecontext.SnapshotMessages(snapshot)
	historyTokens := estimateContextMessagesTokens(messages)
	prefixTokens, err := s.estimatedPromptPrefixTokens(ctx)
	if err != nil {
		return appviewmodel.ContextBudget{}, err
	}
	cfg, ok, err := s.modelConfigForContextBudget(snapshot, modelRef)
	if err != nil {
		return appviewmodel.ContextBudget{}, err
	}
	window, maxOutput := s.contextBudgetLimits(cfg, ok)
	effective := window
	if window > 0 && maxOutput > 0 && maxOutput < window {
		effective = window - maxOutput
	}
	total := historyTokens + prefixTokens
	remaining := effective - total
	overBudget := 0
	if remaining < 0 {
		overBudget = -remaining
		remaining = 0
	}
	lastCompactID, postCompact := latestCompactEventID(snapshot.Events)
	return appviewmodel.ContextBudget{
		Source:                    contextBudgetSourceEstimated,
		ModelID:                   cfg.ID,
		Provider:                  cfg.Provider,
		Model:                     cfg.Model,
		AsOfEventID:               lastCanonicalEventID(snapshot.Events),
		LastCompactEventID:        lastCompactID,
		PostCompact:               postCompact,
		MessageCount:              len(messages),
		ContextWindowTokens:       window,
		MaxOutputTokens:           maxOutput,
		EffectiveInputBudget:      effective,
		EstimatedInputTokens:      total,
		EstimatedHistoryTokens:    historyTokens,
		EstimatedPrefixTokens:     prefixTokens,
		EstimatedRemainingTokens:  remaining,
		EstimatedOverBudgetTokens: overBudget,
	}, nil
}

func (s Services) modelConfigForContextBudget(snapshot session.Snapshot, modelRef string) (appsettings.ModelConfig, bool, error) {
	if modelRef = strings.TrimSpace(modelRef); modelRef != "" {
		if s.settings != nil {
			if cfg, err := s.settings.ResolveModel(modelRef); err == nil {
				return cfg, true, nil
			}
		}
		return appsettings.NormalizeModelConfig(appsettings.ModelConfig{Model: modelRef}), false, nil
	}
	if s.settings == nil {
		return s.runtimeModelConfig(), false, nil
	}
	if modelID, _ := snapshot.State[StateCurrentModelID].(string); strings.TrimSpace(modelID) != "" {
		cfg, err := s.settings.ResolveModel(modelID)
		return cfg, err == nil, err
	}
	cfg, err := s.settings.ResolveModel("")
	if err != nil {
		if strings.Contains(err.Error(), "no model configured") {
			return s.runtimeModelConfig(), false, nil
		}
		return appsettings.ModelConfig{}, false, err
	}
	return cfg, true, nil
}

func (s Services) runtimeModelConfig() appsettings.ModelConfig {
	runtimeCfg := s.Runtime()
	return appsettings.NormalizeModelConfig(appsettings.ModelConfig{
		Model: strings.TrimSpace(runtimeCfg.Model),
	})
}

func (s Services) contextBudgetLimits(cfg appsettings.ModelConfig, configured bool) (int, int) {
	cfg = appsettings.NormalizeModelConfig(cfg)
	caps := s.Models().DefaultCapabilities()
	if configured {
		if found, ok := s.Models().LookupCapabilities(cfg.Provider, cfg.Model); ok {
			caps = found
		}
	}
	window := firstPositive(cfg.ContextWindowTokens, caps.ContextWindowTokens)
	maxOutput := firstPositive(cfg.MaxOutputTokens, caps.DefaultMaxOutputTokens, caps.MaxOutputTokens)
	return window, maxOutput
}

func (s Services) estimatedPromptPrefixTokens(ctx context.Context) (int, error) {
	runtimeCfg := s.Runtime()
	instructions, err := appprompt.BuildInstructions(ctx, appprompt.Config{
		AppName:      runtimeCfg.AppName,
		Catalog:      s.resources,
		PromptPolicy: s.promptPolicy(),
		SkillPolicy:  s.skillPolicy(),
	})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, instruction := range instructions {
		total += estimateContextTextTokens(instruction)
	}
	total += estimateToolAliasTokens(s.resources.Tools)
	return total, nil
}

func estimateToolAliasTokens(tools []plugin.FactoryAlias) int {
	if len(tools) == 0 {
		return 0
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		return len(tools) * 24
	}
	return estimateContextTextTokens(string(raw)) + len(tools)*24
}

func latestCompactEventID(events []session.Event) (string, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if session.IsTransient(events[i]) {
			continue
		}
		if isCompactCheckpoint(events[i]) {
			return strings.TrimSpace(events[i].ID), true
		}
	}
	return "", false
}

func lastCanonicalEventID(events []session.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if session.IsTransient(events[i]) {
			continue
		}
		if id := strings.TrimSpace(events[i].ID); id != "" {
			return id
		}
	}
	return ""
}

func estimateContextMessagesTokens(messages []model.Message) int {
	total := 0
	for _, message := range messages {
		total += estimateContextMessageTokens(message)
	}
	return total
}

func estimateContextMessageTokens(message model.Message) int {
	total := estimateContextTextTokens(string(message.Role)) + 4
	for _, part := range message.Parts {
		total += estimateContextPartTokens(part)
	}
	if message.Origin != nil {
		total += estimateContextTextTokens(message.Origin.Provider)
		total += estimateContextTextTokens(message.Origin.Model)
		total += estimateContextTextTokens(message.Origin.RawFinishReason)
		total += estimateRawJSONMapTokens(message.Origin.Metadata)
	}
	total += estimateAnyMapTokens(message.Meta)
	if total <= 0 {
		return 1
	}
	return total
}

func estimateContextPartTokens(part model.Part) int {
	switch part.Kind {
	case model.PartText:
		if part.Text == nil {
			return 0
		}
		return estimateContextTextTokens(part.Text.Text)
	case model.PartReasoning:
		if part.Reasoning == nil {
			return 0
		}
		total := estimateContextTextTokens(part.Reasoning.VisibleText)
		if part.Reasoning.Replay != nil {
			total += estimateContextReplayTokens(*part.Reasoning.Replay)
		}
		total += estimateRawJSONMapTokens(part.Reasoning.ProviderDetails)
		return total
	case model.PartToolUse:
		if part.ToolUse == nil {
			return 0
		}
		total := estimateContextTextTokens(part.ToolUse.ID)
		total += estimateContextTextTokens(part.ToolUse.Name)
		total += estimateContextTextTokens(string(part.ToolUse.Input))
		if part.ToolUse.Replay != nil {
			total += estimateContextReplayTokens(*part.ToolUse.Replay)
		}
		total += estimateRawJSONMapTokens(part.ToolUse.ProviderDetails)
		return total + 8
	case model.PartToolResult:
		if part.ToolResult == nil {
			return 0
		}
		total := estimateContextTextTokens(part.ToolResult.ToolCallID)
		total += estimateContextTextTokens(part.ToolResult.Name)
		for _, content := range part.ToolResult.Content {
			total += estimateContextPartTokens(content)
		}
		if part.ToolResult.IsError {
			total += 4
		}
		return total + 8
	case model.PartMedia:
		if part.Media == nil {
			return 0
		}
		return 32 +
			estimateContextTextTokens(string(part.Media.Modality)) +
			estimateContextTextTokens(part.Media.MimeType) +
			estimateContextTextTokens(part.Media.Name) +
			estimateContextTextTokens(string(part.Media.Source.Kind)) +
			estimateContextTextTokens(part.Media.Source.URI) +
			estimateContextTextTokens(part.Media.Source.FileID) +
			estimateContextTextTokens(part.Media.Source.LocalRef) +
			estimateContextTextTokens(part.Media.Source.Data)
	case model.PartJSON:
		if part.JSON == nil {
			return 0
		}
		return estimateContextTextTokens(string(part.JSON.Value))
	case model.PartFileRef:
		if part.FileRef == nil {
			return 0
		}
		return 16 +
			estimateContextTextTokens(part.FileRef.Name) +
			estimateContextTextTokens(part.FileRef.MimeType) +
			estimateContextTextTokens(part.FileRef.URI) +
			estimateContextTextTokens(part.FileRef.FileID) +
			estimateContextTextTokens(part.FileRef.LocalRef)
	default:
		return 0
	}
}

func estimateContextReplayTokens(replay model.ReplayMeta) int {
	return estimateContextTextTokens(replay.Provider) +
		estimateContextTextTokens(replay.Kind) +
		estimateContextTextTokens(replay.Token)
}

func estimateRawJSONMapTokens(values map[string]json.RawMessage) int {
	if len(values) == 0 {
		return 0
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return 0
	}
	return estimateContextTextTokens(string(raw))
}

func estimateAnyMapTokens(values map[string]any) int {
	if len(values) == 0 {
		return 0
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return 0
	}
	return estimateContextTextTokens(string(raw))
}

func estimateContextTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	runes := len([]rune(text))
	tokens := runes / 4
	if runes%4 != 0 {
		tokens++
	}
	if tokens <= 0 {
		return 1
	}
	return tokens
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
