package codex

import (
	"context"
	"fmt"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func (s *sessionState) hasServiceTierLocked(value string) bool {
	for _, model := range s.models {
		if modelName(model) != s.model {
			continue
		}
		// Standard is the protocol baseline, not a catalog capability: it stays
		// valid on every known model, including a model advertising no
		// additional service tiers. Catalog IDs are the only Fast-like choices.
		if value == "default" {
			return true
		}
		for _, tier := range model.ServiceTiers {
			if tier.ID == value {
				return true
			}
		}
	}
	return false
}

// serviceTierOptionLocked publishes the protocol Standard baseline for every
// catalog-known model, plus the Fast-like choices that model advertises. A
// catalog entry that lists no additional tiers still offers Standard, and the
// adapter never invents a tier the backend did not advertise. A staged tier
// the catalog no longer advertises omits the option rather than reporting a
// selection the next turn/start would not send.
func (s *sessionState) serviceTierOptionLocked() *acp.SessionConfigOption {
	for _, model := range s.models {
		if modelName(model) != s.model {
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
		// A nil state still omits the request override and only affects the
		// displayed value. Never display Standard while the next turn/start
		// still carries a staged tier the narrowed catalog no longer advertises.
		current := firstNonEmpty(model.DefaultServiceTier, "default")
		if s.serviceTier != nil {
			current = *s.serviceTier
		}
		if !s.hasServiceTierLocked(current) {
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
// Backend admission therefore happens at turn/start; a rejected start keeps the
// staged selection so a retry resends the same complete request. No shared
// app-server configuration is changed.
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
