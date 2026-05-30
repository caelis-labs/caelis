package acpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

const (
	acpConfigModelID     = "model"
	acpConfigReasoningID = "reasoning_effort"
)

func (s *Server) setSessionModel(ctx context.Context, req schema.SetSessionModelRequest) (schema.SetSessionModelResponse, error) {
	if err := s.requireModelService(ctx); err != nil {
		return schema.SetSessionModelResponse{}, err
	}
	_, err := s.services.Models().Use(ctx, s.sessionRef(req.SessionID), req.ModelID, "")
	if err != nil {
		return schema.SetSessionModelResponse{}, err
	}
	return schema.SetSessionModelResponse{}, nil
}

func (s *Server) setSessionConfigOption(ctx context.Context, req schema.SetSessionConfigOptionRequest) (schema.SetSessionConfigOptionResponse, error) {
	if err := s.requireModelService(ctx); err != nil {
		return schema.SetSessionConfigOptionResponse{}, err
	}
	ref := s.sessionRef(req.SessionID)
	switch strings.TrimSpace(req.ConfigID) {
	case acpConfigModelID:
		value, ok := req.Value.(string)
		if !ok {
			return schema.SetSessionConfigOptionResponse{}, fmt.Errorf("surface/acpserver: model value must be a string")
		}
		if _, err := s.services.Models().Use(ctx, ref, value, ""); err != nil {
			return schema.SetSessionConfigOptionResponse{}, err
		}
	case acpConfigReasoningID:
		value, ok := req.Value.(string)
		if !ok {
			return schema.SetSessionConfigOptionResponse{}, fmt.Errorf("surface/acpserver: reasoning effort value must be a string")
		}
		current, ok, err := s.services.Models().Current(ctx, ref)
		if err != nil {
			return schema.SetSessionConfigOptionResponse{}, err
		}
		if !ok {
			return schema.SetSessionConfigOptionResponse{}, fmt.Errorf("surface/acpserver: no current model is configured")
		}
		if _, err := s.services.Models().Use(ctx, ref, current.ID, value); err != nil {
			return schema.SetSessionConfigOptionResponse{}, err
		}
	default:
		return schema.SetSessionConfigOptionResponse{}, fmt.Errorf("surface/acpserver: unsupported config option %q", req.ConfigID)
	}
	resp := schema.SetSessionConfigOptionResponse{}
	if err := s.applySessionMetadata(ctx, ref, &resp.ConfigOptions, nil); err != nil {
		return schema.SetSessionConfigOptionResponse{}, err
	}
	return resp, nil
}

func (s *Server) applySessionMetadata(ctx context.Context, ref session.Ref, configOptions *[]schema.SessionConfigOption, models **schema.SessionModelState) error {
	ref = session.NormalizeRef(ref)
	if ref.AppName == "" {
		ref.AppName = s.appName
	}
	if ref.UserID == "" {
		ref.UserID = s.userID
	}
	if configOptions != nil {
		options, err := s.sessionConfigOptions(ctx, ref)
		if err != nil {
			return err
		}
		*configOptions = options
	}
	if models != nil {
		state, err := s.sessionModelState(ctx, ref)
		if err != nil {
			return err
		}
		*models = state
	}
	return nil
}

func (s *Server) sessionModelState(ctx context.Context, ref session.Ref) (*schema.SessionModelState, error) {
	choices, err := s.services.Models().List(ctx)
	if err != nil || len(choices) == 0 {
		return nil, err
	}
	current, ok, err := s.services.Models().Current(ctx, ref)
	if err != nil {
		return nil, err
	}
	currentID := ""
	if ok {
		currentID = strings.TrimSpace(current.ID)
	}
	models := make([]schema.ModelInfo, 0, len(choices))
	for _, choice := range choices {
		id := strings.TrimSpace(choice.ID)
		if id == "" {
			continue
		}
		if currentID == "" && choice.Default {
			currentID = id
		}
		models = append(models, schema.ModelInfo{
			ModelID:     id,
			Name:        firstNonEmpty(choice.Alias, choice.Model, id),
			Description: strings.TrimSpace(choice.Detail),
		})
	}
	if len(models) == 0 {
		return nil, nil
	}
	if currentID == "" {
		currentID = models[0].ModelID
	}
	return &schema.SessionModelState{
		CurrentModelID:  currentID,
		AvailableModels: models,
	}, nil
}

func (s *Server) sessionConfigOptions(ctx context.Context, ref session.Ref) ([]schema.SessionConfigOption, error) {
	choices, err := s.services.Models().List(ctx)
	if err != nil || len(choices) == 0 {
		return nil, err
	}
	current, ok, err := s.services.Models().Current(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	options := []schema.SessionConfigOption{{
		Type:         "select",
		ID:           acpConfigModelID,
		Name:         "Model",
		Description:  "Choose which configured model Caelis should use",
		Category:     "model",
		CurrentValue: current.ID,
		Options:      modelSelectOptions(choices),
	}}
	levels := reasoningLevels(current)
	if len(levels) > 0 {
		options = append(options, schema.SessionConfigOption{
			Type:         "select",
			ID:           acpConfigReasoningID,
			Name:         "Reasoning Effort",
			Description:  "Choose how much reasoning effort the model should use",
			Category:     "thought_level",
			CurrentValue: s.currentReasoningEffort(ctx, ref, current, levels),
			Options:      reasoningSelectOptions(levels),
		})
	}
	return options, nil
}

func (s *Server) currentReasoningEffort(ctx context.Context, ref session.Ref, cfg appsettings.ModelConfig, levels []string) string {
	if snapshot, err := s.engine.LoadSession(ctx, ref); err == nil {
		if value, _ := snapshot.State[appservices.StateCurrentReasoningEffort].(string); strings.TrimSpace(value) != "" {
			return strings.ToLower(strings.TrimSpace(value))
		}
	}
	for _, value := range []string{cfg.DefaultReasoningEffort, cfg.ReasoningEffort} {
		value = strings.ToLower(strings.TrimSpace(value))
		if containsFold(levels, value) {
			return value
		}
	}
	if len(levels) > 0 {
		return levels[0]
	}
	return ""
}

func (s *Server) requireModelService(ctx context.Context) error {
	choices, err := s.services.Models().List(ctx)
	if err != nil {
		return err
	}
	if len(choices) == 0 {
		return errors.New("surface/acpserver: model service is not configured")
	}
	return nil
}

func modelSelectOptions(choices []appsettings.ModelChoice) []schema.SessionConfigSelectOption {
	if len(choices) == 0 {
		return nil
	}
	out := make([]schema.SessionConfigSelectOption, 0, len(choices))
	for _, choice := range choices {
		id := strings.TrimSpace(choice.ID)
		if id == "" {
			continue
		}
		out = append(out, schema.SessionConfigSelectOption{
			Value:       id,
			Name:        firstNonEmpty(choice.Alias, choice.Model, id),
			Description: strings.TrimSpace(choice.Detail),
		})
	}
	return out
}

func reasoningLevels(cfg appsettings.ModelConfig) []string {
	levels := appsettings.DedupeStrings(cfg.ReasoningLevels)
	if len(levels) == 0 {
		switch strings.ToLower(strings.TrimSpace(cfg.ReasoningMode)) {
		case "toggle":
			levels = []string{"none", "high", "max"}
		case "fixed":
			levels = []string{"low", "medium", "high"}
		}
	}
	return appsettings.DedupeStrings(levels)
}

func reasoningSelectOptions(levels []string) []schema.SessionConfigSelectOption {
	if len(levels) == 0 {
		return nil
	}
	out := make([]schema.SessionConfigSelectOption, 0, len(levels))
	for _, level := range levels {
		value := strings.ToLower(strings.TrimSpace(level))
		if value == "" {
			continue
		}
		out = append(out, schema.SessionConfigSelectOption{
			Value: value,
			Name:  titleToken(value),
		})
	}
	return out
}

func containsFold(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func titleToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
