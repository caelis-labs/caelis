package acpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func (s *Server) setSettingsConfigOption(ctx context.Context, req schema.SetSessionConfigOptionRequest) (bool, error) {
	id := strings.TrimSpace(req.ConfigID)
	if !s.services.Settings().Configured() {
		return false, nil
	}
	options, err := s.services.Settings().ConfigOptions(ctx)
	if err != nil {
		return true, err
	}
	fieldID, ok := settingsConfigFieldID(options, id)
	if !ok {
		return false, nil
	}
	value, ok := acpConfigValueString(req.Value)
	if !ok {
		return true, fmt.Errorf("surface/acpserver: %s value must be a string or number", id)
	}
	_, err = s.services.Settings().SetPanelField(ctx, appservices.SettingsPanelFieldUpdateRequest{
		SessionRef: s.sessionRef(req.SessionID),
		FieldID:    fieldID,
		Value:      value,
	})
	return true, err
}

func settingsConfigFieldID(options []appviewmodel.SettingsConfigOption, id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false
	}
	for _, option := range options {
		if option.ID == id && strings.TrimSpace(option.FieldID) != "" {
			return option.FieldID, true
		}
	}
	return "", false
}

func acpConfigValueString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), true
	case int:
		return strconv.Itoa(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32), true
	case json.Number:
		return typed.String(), true
	default:
		return "", false
	}
}

func (s *Server) sessionSettingsConfigOptions(ctx context.Context) ([]schema.SessionConfigOption, error) {
	if !s.services.Settings().Configured() {
		return nil, nil
	}
	options, err := s.services.Settings().ConfigOptions(ctx)
	if err != nil {
		return nil, err
	}
	return acpSettingsConfigOptions(options), nil
}

func acpSettingsConfigOptions(in []appviewmodel.SettingsConfigOption) []schema.SessionConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.SessionConfigOption, 0, len(in))
	for _, option := range in {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		out = append(out, schema.SessionConfigOption{
			Type:         strings.TrimSpace(option.Type),
			ID:           id,
			Name:         strings.TrimSpace(option.Name),
			Description:  strings.TrimSpace(option.Description),
			Category:     strings.TrimSpace(option.Category),
			CurrentValue: option.CurrentValue,
			Options:      acpSettingsConfigSelectOptions(option.Options),
		})
	}
	return out
}

func acpSettingsConfigSelectOptions(in []appviewmodel.SettingsConfigOptionChoice) []schema.SessionConfigSelectOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.SessionConfigSelectOption, 0, len(in))
	for _, option := range in {
		value := strings.TrimSpace(option.Value)
		if value == "" {
			continue
		}
		out = append(out, schema.SessionConfigSelectOption{
			Value:       value,
			Name:        firstNonEmpty(option.Name, value),
			Description: strings.TrimSpace(option.Description),
		})
	}
	return out
}
