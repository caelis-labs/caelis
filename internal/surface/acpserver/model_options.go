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
	acpConfigModeID      = "mode"
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
	ref := s.sessionRef(req.SessionID)
	switch strings.TrimSpace(req.ConfigID) {
	case acpConfigModeID:
		value, ok := req.Value.(string)
		if !ok {
			return schema.SetSessionConfigOptionResponse{}, fmt.Errorf("surface/acpserver: mode value must be a string")
		}
		if _, err := s.setSessionMode(ctx, schema.SetSessionModeRequest{SessionID: req.SessionID, ModeID: value}); err != nil {
			return schema.SetSessionConfigOptionResponse{}, err
		}
	case acpConfigModelID:
		if err := s.requireModelService(ctx); err != nil {
			return schema.SetSessionConfigOptionResponse{}, err
		}
		value, ok := req.Value.(string)
		if !ok {
			return schema.SetSessionConfigOptionResponse{}, fmt.Errorf("surface/acpserver: model value must be a string")
		}
		if _, err := s.services.Models().Use(ctx, ref, value, ""); err != nil {
			return schema.SetSessionConfigOptionResponse{}, err
		}
	case acpConfigReasoningID:
		if err := s.requireModelService(ctx); err != nil {
			return schema.SetSessionConfigOptionResponse{}, err
		}
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
	if err := s.applySessionMetadata(ctx, ref, &resp.ConfigOptions, nil, nil); err != nil {
		return schema.SetSessionConfigOptionResponse{}, err
	}
	return resp, nil
}

func (s *Server) applySessionMetadata(ctx context.Context, ref session.Ref, configOptions *[]schema.SessionConfigOption, models **schema.SessionModelState, modes **schema.SessionModeState) error {
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
	if modes != nil {
		state, err := s.sessionModeState(ctx, ref)
		if err != nil {
			return err
		}
		*modes = state
	}
	return nil
}

func (s *Server) sessionModeState(ctx context.Context, ref session.Ref) (*schema.SessionModeState, error) {
	if s.services.Engine() == nil {
		return nil, nil
	}
	choices, err := s.services.Modes().List(ctx)
	if err != nil || len(choices) == 0 {
		return nil, err
	}
	currentID, err := s.services.Modes().CurrentID(ctx, ref)
	if err != nil {
		return nil, err
	}
	modes := make([]schema.SessionMode, 0, len(choices))
	for _, choice := range choices {
		id := strings.TrimSpace(choice.ID)
		if id == "" {
			continue
		}
		modes = append(modes, schema.SessionMode{
			ID:          id,
			Name:        strings.TrimSpace(choice.Name),
			Description: strings.TrimSpace(choice.Description),
		})
	}
	if len(modes) == 0 {
		return nil, nil
	}
	return &schema.SessionModeState{
		AvailableModes: modes,
		CurrentModeID:  strings.TrimSpace(currentID),
	}, nil
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
	options := []schema.SessionConfigOption{}
	if modeOption, ok, err := s.sessionModeConfigOption(ctx, ref); err != nil {
		return nil, err
	} else if ok {
		options = append(options, modeOption)
	}
	choices, err := s.services.Models().List(ctx)
	if err != nil || len(choices) == 0 {
		return options, err
	}
	current, ok, err := s.services.Models().Current(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !ok {
		return options, nil
	}
	options = append(options, schema.SessionConfigOption{
		Type:         "select",
		ID:           acpConfigModelID,
		Name:         "Model",
		Description:  "Choose which configured model Caelis should use",
		Category:     "model",
		CurrentValue: current.ID,
		Options:      modelSelectOptions(choices),
	})
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

func (s *Server) sessionModeConfigOption(ctx context.Context, ref session.Ref) (schema.SessionConfigOption, bool, error) {
	state, err := s.sessionModeState(ctx, ref)
	if err != nil || state == nil || len(state.AvailableModes) == 0 {
		return schema.SessionConfigOption{}, false, err
	}
	return schema.SessionConfigOption{
		Type:         "select",
		ID:           acpConfigModeID,
		Name:         "Approval Preset",
		Description:  "Choose approval behavior for this session",
		Category:     "mode",
		CurrentValue: state.CurrentModeID,
		Options:      modeSelectOptions(state.AvailableModes),
	}, true, nil
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

func modeSelectOptions(modes []schema.SessionMode) []schema.SessionConfigSelectOption {
	if len(modes) == 0 {
		return nil
	}
	out := make([]schema.SessionConfigSelectOption, 0, len(modes))
	for _, mode := range modes {
		id := strings.TrimSpace(mode.ID)
		if id == "" {
			continue
		}
		out = append(out, schema.SessionConfigSelectOption{
			Value:       id,
			Name:        firstNonEmpty(mode.Name, id),
			Description: strings.TrimSpace(mode.Description),
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
