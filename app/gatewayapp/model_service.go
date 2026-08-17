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

func (s *runtimeComposition) setRuntimeDefaultModelFromLookup() {
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

func (s *runtimeComposition) setRuntimeModel(profileID string, cfg ModelConfig) {
	if s == nil || s.process == nil || s.process.config == nil {
		return
	}
	runtimeCfg := s.runtimeProcessSnapshot().runtime
	runtimeCfg.ModelProfileID = modelprofile.NormalizeID(profileID)
	runtimeCfg.ModelProfileEffort = strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
	runtimeCfg.Model = cfg
	s.process.config.setRuntime(runtimeCfg)
}

// ListModelAliases returns the current session override plus resolver-known
// model aliases for picker surfaces such as the TUI `/model` command.
func (s *runtimeComposition) ListModelAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
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

func (s *runtimeComposition) ListModelChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
	if s == nil || s.sessions == nil {
		return nil, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if s.lookup == nil {
		return nil, fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	choices := make([]ModelChoice, 0, len(s.lookup.ListModelChoices())+1)
	if strings.TrimSpace(ref.SessionID) != "" {
		state, err := s.sessions.SnapshotState(ctx, ref)
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
func (s *runtimeComposition) HasReusableProviderAuth(ctx context.Context, provider string, baseURL string) bool {
	if s == nil {
		return false
	}
	_, reusable := s.reusableProviderCredentialRef(ctx, s.lookup, provider, "", baseURL)
	return reusable
}

func (s *runtimeComposition) DefaultModelAlias() string {
	if s == nil || s.lookup == nil {
		return ""
	}
	return s.lookup.DefaultAlias()
}

func (s *runtimeComposition) DefaultModelEffort() string {
	if s == nil || s.lookup == nil {
		return ""
	}
	return s.lookup.DefaultEffort()
}

// EffectiveModelAlias returns the model selected for new work in this Host
// process. Startup ModelProfile flags may intentionally make it differ from
// the persisted Host default without mutating AppConfig.
func (s *runtimeComposition) EffectiveModelAlias() string {
	if s == nil {
		return ""
	}
	runtimeModel := s.runtimeProcessSnapshot().runtime.Model
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
func (s *runtimeComposition) EffectiveModelEffort() string {
	if s == nil {
		return ""
	}
	runtimeCfg := s.runtimeProcessSnapshot().runtime
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

func (s *runtimeComposition) ModelConfig(alias string) (ModelConfig, bool) {
	if s == nil || s.lookup == nil {
		return ModelConfig{}, false
	}
	return s.lookup.Config(alias)
}
