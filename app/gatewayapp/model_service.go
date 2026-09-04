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
	s.setRuntimeModel(profileID, cfg, s.lookup.DefaultFastMode())
}

func (s *runtimeComposition) setRuntimeDefaultProfile(profiles modelprofile.Configuration) {
	if s == nil {
		return
	}
	profiles = modelprofile.NormalizeConfiguration(profiles)
	profile, ok := modelprofile.Lookup(profiles, profiles.DefaultProfileID)
	if !ok {
		s.setRuntimeModel("", ModelConfig{}, false)
		return
	}
	cfg := ModelConfig{}
	if profile.Kind() == modelprofile.BackendProvider && s.lookup != nil {
		cfg, _ = s.lookup.Config(profile.Backend.Provider.ModelConfigID)
		cfg.ReasoningEffort = profiles.DefaultEffort
	}
	s.setRuntimeModel(profile.ID, cfg, profiles.DefaultFastMode)
	if s.process != nil && s.process.config != nil {
		runtimeCfg := s.runtimeProcessSnapshot().runtime
		runtimeCfg.ModelProfileEffort = profiles.DefaultEffort
		runtimeCfg.ModelFastMode = profiles.DefaultFastMode
		s.process.config.setRuntime(runtimeCfg)
	}
}

func (s *runtimeComposition) setRuntimeModel(profileID string, cfg ModelConfig, fastMode bool) {
	if s == nil || s.process == nil || s.process.config == nil {
		return
	}
	runtimeCfg := s.runtimeProcessSnapshot().runtime
	runtimeCfg.ModelProfileID = modelprofile.NormalizeID(profileID)
	runtimeCfg.ModelProfileEffort = strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
	runtimeCfg.ModelFastMode = fastMode
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
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	profiles := modelprofile.NormalizeConfiguration(snapshot.placement.Profiles)
	choices := make([]ModelChoice, 0, len(profiles.Profiles)+1)
	if strings.TrimSpace(ref.SessionID) != "" {
		state, err := s.sessions.SnapshotState(ctx, ref)
		if err != nil {
			return nil, err
		}
		if modelRef := kernelimpl.CurrentModelAlias(state); modelRef != "" {
			if cfg, ok := s.lookup.Config(modelRef); ok {
				choice := modelChoiceFromConfig(cfg)
				choice.ProfileID = modelprofile.BuildProviderID(cfg.ID)
				choices = append(choices, choice)
			}
		}
	}
	ordered := profiles.Profiles
	if profiles.DefaultProfileID != "" {
		ordered = make([]modelprofile.ModelProfile, 0, len(profiles.Profiles))
		if selected, ok := modelprofile.Lookup(profiles, profiles.DefaultProfileID); ok {
			ordered = append(ordered, selected)
		}
		for _, profile := range profiles.Profiles {
			if profile.ID != profiles.DefaultProfileID {
				ordered = append(ordered, profile)
			}
		}
	}
	for _, profile := range ordered {
		switch profile.Kind() {
		case modelprofile.BackendProvider:
			cfg, ok := s.lookup.Config(profile.Backend.Provider.ModelConfigID)
			if !ok {
				continue
			}
			choice := modelChoiceFromConfig(cfg)
			choice.ProfileID = profile.ID
			choice.ReasoningLevels = modelProfileEfforts(profile)
			choices = append(choices, choice)
		case modelprofile.BackendACP:
			choices = append(choices, ModelChoice{
				ID:              profile.ID,
				Alias:           profile.DisplayName,
				ProfileID:       profile.ID,
				Backend:         string(modelprofile.BackendACP),
				Provider:        profile.Backend.ACP.AgentID,
				Model:           profile.Backend.ACP.RemoteModelID,
				Detail:          fmt.Sprintf("ACP Agent %s · %s", profile.Backend.ACP.AgentID, profile.Backend.ACP.RemoteModelID),
				ReasoningLevels: modelProfileEfforts(profile),
			})
		}
	}
	// Keep a bounded compatibility path for provider configs created before the
	// unified ModelProfile catalog. Canonical profile choices win deduplication.
	for _, choice := range s.lookup.ListModelChoices() {
		choice.ProfileID = modelprofile.BuildProviderID(choice.ID)
		choices = append(choices, choice)
	}
	return dedupeModelChoices(choices), nil
}

func modelProfileEfforts(profile modelprofile.ModelProfile) []string {
	profile = modelprofile.Normalize(profile)
	levels := make([]string, 0, len(profile.Effort.Choices))
	for _, choice := range profile.Effort.Choices {
		levels = append(levels, choice.Canonical)
	}
	return dedupeNonEmptyStrings(levels)
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
	s.placementCacheMu.RLock()
	cached := s.placementCache
	s.placementCacheMu.RUnlock()
	if cached != nil {
		profiles := modelprofile.NormalizeConfiguration(cached.placement.Profiles)
		profile, ok := modelprofile.Lookup(profiles, profiles.DefaultProfileID)
		if !ok {
			return ""
		}
		if profile.Kind() == modelprofile.BackendACP {
			return profile.DisplayName
		}
		if profile.Backend.Provider != nil {
			if configured, ok := s.lookup.Config(profile.Backend.Provider.ModelConfigID); ok {
				return configured.Alias
			}
		}
		return ""
	}
	return s.lookup.DefaultAlias()
}

func (s *runtimeComposition) DefaultModelEffort() string {
	if s == nil || s.lookup == nil {
		return ""
	}
	s.placementCacheMu.RLock()
	cached := s.placementCache
	s.placementCacheMu.RUnlock()
	if cached != nil {
		return modelprofile.NormalizeConfiguration(cached.placement.Profiles).DefaultEffort
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
	runtimeProfileID := s.runtimeProcessSnapshot().runtime.ModelProfileID
	if alias := strings.TrimSpace(runtimeModel.Alias); alias != "" {
		return alias
	}
	if id := strings.TrimSpace(runtimeModel.ID); id != "" && s.lookup != nil {
		if cfg, ok := s.lookup.Config(id); ok {
			return strings.TrimSpace(cfg.Alias)
		}
	}
	if profile, ok := s.cachedModelProfile(runtimeProfileID); ok {
		return profile.DisplayName
	}
	return s.DefaultModelAlias()
}

func (s *runtimeComposition) cachedDefaultProfileID() string {
	if s == nil {
		return ""
	}
	s.placementCacheMu.RLock()
	defer s.placementCacheMu.RUnlock()
	if s.placementCache == nil {
		return ""
	}
	return s.placementCache.placement.Profiles.DefaultProfileID
}

func (s *runtimeComposition) cachedModelProfile(profileID string) (modelprofile.ModelProfile, bool) {
	if s == nil {
		return modelprofile.ModelProfile{}, false
	}
	s.placementCacheMu.RLock()
	defer s.placementCacheMu.RUnlock()
	if s.placementCache == nil {
		return modelprofile.ModelProfile{}, false
	}
	return modelprofile.Lookup(s.placementCache.placement.Profiles, profileID)
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

// EffectiveModelFastMode reports whether new work in this Host uses the
// priority service tier.
func (s *runtimeComposition) EffectiveModelFastMode() bool {
	if s == nil {
		return false
	}
	return s.runtimeProcessSnapshot().runtime.ModelFastMode
}

func (s *runtimeComposition) ModelConfig(alias string) (ModelConfig, bool) {
	if s == nil || s.lookup == nil {
		return ModelConfig{}, false
	}
	return s.lookup.Config(alias)
}
