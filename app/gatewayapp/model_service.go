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

// HasReusableProviderAuth reports whether a configured provider endpoint still
// references usable Host authentication. A running invocation may retain the
// API-key material it already resolved; deleting the last ModelProfile retires
// the Host credential and prevents later work from resolving that profile.
func (s *runtimeComposition) HasReusableProviderAuth(ctx context.Context, provider string, baseURL string) bool {
	if s == nil || s.authorities.store == nil {
		return false
	}
	if err := recoverProviderCredentialRetirements(ctx, s.authorities.store, s.authorities.apiKeyCredentials); err != nil {
		return false
	}
	doc, err := s.authorities.store.LoadContext(contextOrBackground(ctx))
	if err != nil {
		return false
	}
	lookup, err := newModelLookupFromDocument(doc, 0)
	if err != nil {
		return false
	}
	_, reusable := s.reusableProviderCredentialRef(ctx, lookup, doc.ModelProfiles, provider, "", baseURL)
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
