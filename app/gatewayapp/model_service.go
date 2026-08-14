package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelprofile"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

func wrapOptionalError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func (s *Stack) setRuntimeDefaultModelFromLookup() {
	if s == nil || s.lookup == nil {
		return
	}
	cfg := ModelConfig{}
	profileID := ""
	if defaultID := s.lookup.DefaultID(); strings.TrimSpace(defaultID) != "" {
		cfg, _ = s.lookup.Config(defaultID)
		profileID = modelprofile.BuildProviderID(defaultID)
		cfg.ReasoningEffort = s.lookup.DefaultEffort()
	}
	s.setRuntimeModel(profileID, cfg)
}

func (s *Stack) setRuntimeModel(profileID string, cfg ModelConfig) {
	if s == nil {
		return
	}
	s.mu.Lock()
	runtimeCfg := s.runtime
	runtimeCfg.ModelProfileID = modelprofile.NormalizeID(profileID)
	runtimeCfg.ModelProfileEffort = strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
	runtimeCfg.Model = cfg
	s.runtime = runtimeCfg
	s.mu.Unlock()
}

// ListModelAliases returns the current session override plus resolver-known
// model aliases for picker surfaces such as the TUI `/model` command.
func (s *Stack) ListModelAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	choices, err := s.ListModelChoices(ctx, ref)
	if err != nil {
		return nil, err
	}
	aliases := make([]string, 0, len(choices))
	for _, choice := range choices {
		aliases = append(aliases, choice.Alias)
	}
	return dedupeNonEmptyStrings(aliases), nil
}

func (s *Stack) ListModelChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
	if s == nil || s.Sessions == nil {
		return nil, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if s.lookup == nil {
		return nil, fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	choices := make([]ModelChoice, 0, len(s.lookup.ListModelChoices())+1)
	if strings.TrimSpace(ref.SessionID) != "" {
		state, err := s.Sessions.SnapshotState(ctx, ref)
		if err != nil {
			return nil, err
		}
		if modelRef := kernelimpl.CurrentModelAlias(state); modelRef != "" {
			if cfg, ok := s.lookup.Config(modelRef); ok {
				choices = append(choices, modelChoiceFromConfig(cfg))
			}
		}
	}
	choices = append(choices, s.lookup.ListModelChoices()...)
	return dedupeModelChoices(choices), nil
}

// HasReusableProviderAuth reports whether Control retains usable Host
// authentication for the provider endpoint. API-key credentials outlive model
// catalog entries so deleting a model cannot invalidate pinned Runtime
// generations. Invalid legacy credentials must not make /connect skip API-key
// collection.
func (s *Stack) HasReusableProviderAuth(ctx context.Context, provider string, baseURL string) bool {
	if s == nil {
		return false
	}
	_, reusable := s.reusableProviderCredentialRef(ctx, s.lookup, provider, "", baseURL)
	return reusable
}

func (s *Stack) DefaultModelAlias() string {
	if s == nil || s.lookup == nil {
		return ""
	}
	return s.lookup.DefaultAlias()
}

func (s *Stack) DefaultModelEffort() string {
	if s == nil || s.lookup == nil {
		return ""
	}
	return s.lookup.DefaultEffort()
}

// EffectiveModelAlias returns the model selected for new work in this Host
// process. Startup ModelProfile flags may intentionally make it differ from
// the persisted Host default without mutating AppConfig.
func (s *Stack) EffectiveModelAlias() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	runtimeModel := s.runtime.Model
	s.mu.RUnlock()
	if alias := strings.TrimSpace(runtimeModel.Alias); alias != "" {
		return alias
	}
	if id := strings.TrimSpace(runtimeModel.ID); id != "" && s.lookup != nil {
		if cfg, ok := s.lookup.Config(id); ok {
			return strings.TrimSpace(cfg.Alias)
		}
	}
	return s.DefaultModelAlias()
}

// EffectiveModelEffort returns the reasoning effort selected for new work in
// this Host process without conflating it with the persisted Host default.
func (s *Stack) EffectiveModelEffort() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	runtimeCfg := s.runtime
	s.mu.RUnlock()
	if effort := strings.TrimSpace(runtimeCfg.ModelProfileEffort); effort != "" {
		return effort
	}
	if effort := strings.TrimSpace(runtimeCfg.Model.ReasoningEffort); effort != "" {
		return effort
	}
	if strings.EqualFold(s.EffectiveModelAlias(), s.DefaultModelAlias()) {
		return s.DefaultModelEffort()
	}
	return ""
}

func (s *Stack) ModelConfig(alias string) (ModelConfig, bool) {
	if s == nil || s.lookup == nil {
		return ModelConfig{}, false
	}
	return s.lookup.Config(alias)
}
