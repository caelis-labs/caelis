package codex

import (
	"context"
	"fmt"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func (s *sessionState) hasServiceTierLocked(value string) bool {
	for _, model := range s.models {
		if modelName(model) == s.model {
			if value == "default" && len(model.ServiceTiers) > 0 {
				return true
			}
			for _, tier := range model.ServiceTiers {
				if tier.ID == value {
					return true
				}
			}
		}
	}
	return false
}

func (s *sessionState) serviceTierOptionLocked() *acp.SessionConfigOption {
	for _, model := range s.models {
		if modelName(model) != s.model || len(model.ServiceTiers) == 0 {
			continue
		}
		values := acp.SessionConfigSelectOptionsUngrouped{{Value: "default", Name: "Standard", Description: acp.Ptr("Explicit standard speed")}}
		for _, tier := range model.ServiceTiers {
			if strings.TrimSpace(tier.ID) == "" || tier.ID == "default" {
				continue
			}
			values = append(values, acp.SessionConfigSelectOption{
				Value: acp.SessionConfigValueId(tier.ID), Name: firstNonEmpty(tier.Name, tier.ID),
				Description: optionalString(tier.Description),
			})
		}
		current := firstNonEmpty(model.DefaultServiceTier, "default")
		if s.serviceTier != nil {
			current = *s.serviceTier
		}
		// Standard is the protocol-defined baseline, while catalog entries
		// describe additional tiers. A nil state still omits the request override.
		if len(values) == 0 || !s.hasServiceTierLocked(current) {
			return nil
		}
		option := acp.NewSessionConfigOptionSelect(acp.SessionConfigValueId(current), acp.SessionConfigSelectOptions{Ungrouped: &values})
		option.Select.Id = acp.SessionConfigId(configIDServiceTier)
		option.Select.Name = "Service tier"
		return &option
	}
	return nil
}

// setServiceTier stages a validated next-Turn selection, like model and effort.
// Codex cannot resume a newly opened thread before its first persisted Turn.
// Backend admission therefore happens at turn/start; a rejected start restores
// the last effective selection. No shared app-server configuration is changed.
func (a *agent) setServiceTier(ctx context.Context, state *sessionState, value string) (acp.SetSessionConfigOptionResponse, error) {
	if !state.promptMu.TryLock() {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("codex adapter: service tier cannot change during a turn")
	}
	defer state.promptMu.Unlock()
	if err := a.loadModels(ctx, state); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if !state.hasServiceTierLocked(value) {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "unsupported service tier"})
	}
	state.serviceTier = acp.Ptr(value)
	return acp.SetSessionConfigOptionResponse{ConfigOptions: state.configOptionsLocked()}, nil
}
