package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
)

// removedModelProfiles keeps ACP profile identities separate from the provider
// config IDs used by the resolver and native Session state.
type removedModelProfiles struct {
	providers       map[string]string
	acp             map[string]struct{}
	beforeProviders []ModelConfig
}

func (r removedModelProfiles) hasProvider(id string) bool {
	_, ok := r.providers[modelprofile.BuildProviderID(id)]
	return ok
}

func (r removedModelProfiles) providerIDForSelector(selector string) string {
	if r.hasProvider(selector) {
		return selector
	}
	if cfg, ok, _ := modelconfig.ResolveSelector(r.beforeProviders, selector); ok && r.hasProvider(cfg.ID) {
		return cfg.ID
	}
	return ""
}

func (s *controlCommandBackend) deleteHostModelAtRevision(ctx context.Context, alias string, expected *uint64) (hostModelMutationResult, error) {
	doc, candidate, err := s.loadModelConfigurationCandidate(ctx, expected)
	if err != nil {
		return hostModelMutationResult{Revision: configurationErrorRevision(err, doc.ConfigurationRevision)}, err
	}
	cfg, err := candidate.ResolveConfig(alias)
	if err != nil {
		return hostModelMutationResult{Revision: doc.ConfigurationRevision}, err
	}
	// The single-model API also accepts legacy provider selectors and configs
	// that predate the unified ModelProfile catalog.
	return s.deleteModelSelection(ctx, doc, candidate, removedModelProfiles{
		providers: map[string]string{modelprofile.BuildProviderID(cfg.ID): cfg.ID},
	})
}

func (s *controlCommandBackend) deleteHostModelProfilesAtRevision(ctx context.Context, profileIDs []string, expected *uint64) (hostModelMutationResult, error) {
	doc, candidate, err := s.loadModelConfigurationCandidate(ctx, expected)
	if err != nil {
		return hostModelMutationResult{Revision: configurationErrorRevision(err, doc.ConfigurationRevision)}, err
	}
	removed := removedModelProfiles{providers: map[string]string{}, acp: map[string]struct{}{}}
	// The catalog still advertises synthetic provider IDs for legacy configs.
	// Index their exact config IDs so an alias cannot delete a different model.
	legacyProviders := make(map[string]string)
	for _, configured := range candidate.Snapshot().Configs {
		legacyProviders[modelprofile.BuildProviderID(configured.ID)] = configured.ID
	}
	profiles := make(map[string]modelprofile.ModelProfile, len(doc.ModelProfiles.Profiles))
	for _, profile := range modelprofile.NormalizeConfiguration(doc.ModelProfiles).Profiles {
		profiles[profile.ID] = profile
	}
	for _, id := range profileIDs {
		id = modelprofile.NormalizeID(id)
		profile, exists := profiles[id]
		if !exists {
			if configID, present := legacyProviders[id]; present {
				removed.providers[id] = configID
				continue
			}
			return hostModelMutationResult{Revision: doc.ConfigurationRevision}, errorcode.New(errorcode.InvalidArgument, fmt.Sprintf("gatewayapp: unknown model profile %q", id))
		}
		switch profile.Kind() {
		case modelprofile.BackendProvider:
			removed.providers[id] = profile.Backend.Provider.ModelConfigID
		case modelprofile.BackendACP:
			removed.acp[id] = struct{}{}
		}
	}
	return s.deleteModelSelection(ctx, doc, candidate, removed)
}

func (s *controlCommandBackend) deleteModelSelection(ctx context.Context, doc AppConfig, candidate *modelLookup, removed removedModelProfiles) (result hostModelMutationResult, returnErr error) {
	beforeDoc := doc
	removed.beforeProviders = doc.Models.Configs
	result.Revision = doc.ConfigurationRevision
	defaultID := modelprofile.NormalizeID(doc.ModelProfiles.DefaultProfileID)
	for profileID := range removed.providers {
		doc.AgentBindings, _ = agentbinding.RemoveProfileBindings(doc.AgentBindings, profileID)
		doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, profileID)
	}
	for profileID := range removed.acp {
		doc.AgentBindings, _ = agentbinding.RemoveProfileBindings(doc.AgentBindings, profileID)
		doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, profileID)
	}
	// Profile IDs and provider configs have independent identities in saved
	// configuration. Drop a config only once its last profile is removed.
	remainingProviderConfigs := make(map[string]struct{})
	for _, profile := range doc.ModelProfiles.Profiles {
		if provider := profile.Backend.Provider; provider != nil {
			remainingProviderConfigs[modelprofile.BuildProviderID(provider.ModelConfigID)] = struct{}{}
		}
	}
	deletedProviderConfigs := make(map[string]string, len(removed.providers))
	for _, modelID := range removed.providers {
		key := modelprofile.BuildProviderID(modelID)
		if _, retained := remainingProviderConfigs[key]; retained {
			continue
		}
		if _, deleted := deletedProviderConfigs[key]; deleted {
			continue
		}
		if err := candidate.Delete(modelID); err != nil {
			return result, err
		}
		deletedProviderConfigs[key] = modelID
	}
	removed.providers = deletedProviderConfigs
	if defaultID != "" && doc.ModelProfiles.DefaultProfileID == "" {
		for _, profile := range doc.ModelProfiles.Profiles {
			if profile.Judgment {
				continue
			}
			var err error
			doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, profile.ID, "")
			if err != nil {
				return result, err
			}
			break
		}
	}
	if profile, ok := modelprofile.Lookup(doc.ModelProfiles, doc.ModelProfiles.DefaultProfileID); ok && profile.Backend.Provider != nil {
		candidate.SetDefaultWithFastMode(profile.Backend.Provider.ModelConfigID, doc.ModelProfiles.DefaultEffort, doc.ModelProfiles.DefaultFastMode)
	}
	doc.Models = candidate.Snapshot()
	if profile, ok := modelprofile.Lookup(doc.ModelProfiles, doc.ModelProfiles.DefaultProfileID); !ok || profile.Backend.Provider == nil {
		doc.Models.DefaultAlias = ""
		doc.Models.DefaultID = ""
		doc.Models.DefaultEffort = ""
		doc.Models.DefaultFastMode = false
	}

	credentialTxn, err := s.prepareProviderCredentialRetirement(
		ctx,
		retiredAPIKeyCredentialRefs(beforeDoc, doc),
		beforeDoc.ConfigurationRevision,
	)
	if err != nil {
		if errors.Is(err, credentialstore.ErrRollbackIncomplete) {
			result.EffectStarted = true
		}
		return result, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = rollbackModelCredentials(credentialTxn, returnErr, &result)
		}
	}()

	saved, persistWarning, persistErr := s.persistModelConfiguration(ctx, doc)
	if persistErr != nil {
		result.EffectStarted = configstore.WriteCommitted(persistErr)
		result.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
		if result.EffectStarted {
			persistErr = errors.Join(
				persistErr,
				wrapOptionalError("gatewayapp: commit retired provider credentials", credentialTxn.commit()),
				s.observeCommittedModelConfiguration(ctx, saved),
				wrapOptionalError("gatewayapp: reconcile active Session model removal", s.reconcileDeletedModelProfiles(ctx, saved.ConfigurationRevision, removed)),
			)
		}
		return result, persistErr
	}
	credentialCommitWarning := wrapOptionalError("gatewayapp: commit retired provider credentials", credentialTxn.commit())
	result.EffectStarted = true
	result.Revision = saved.ConfigurationRevision
	result.Warning = errors.Join(
		persistWarning,
		credentialCommitWarning,
		s.observeCommittedModelConfiguration(ctx, saved),
		s.reconcileDeletedModelProfiles(ctx, saved.ConfigurationRevision, removed),
	)
	return result, nil
}

// The Host catalog and pins are published before waiting on Runtime activation.
// Under that fence, repair live Runtimes and durable Sessions from the current
// configuration, which may already include a reconnected model.
func (s *controlCommandBackend) reconcileDeletedModelProfiles(ctx context.Context, committedRevision uint64, removed removedModelProfiles) error {
	// The write is durable. Finish revocation even if the initiating client
	// disconnects, while holding the Runtime activation fence.
	reconcileCtx := context.WithoutCancel(contextOrBackground(ctx))
	runtimes := s.runtimeRegistry()
	if runtimes != nil {
		var unlock func()
		var err error
		reconcileCtx, unlock, err = runtimes.lockActivation(reconcileCtx)
		if err != nil {
			return fmt.Errorf("gatewayapp: serialize Session Runtime model removal: %w", err)
		}
		defer unlock()
	}

	for attempt := 0; attempt < sessionModelRecoveryMaxAttempts; attempt++ {
		doc, err := s.composition.authorities.store.LoadContext(reconcileCtx)
		if err != nil {
			return fmt.Errorf("gatewayapp: read canonical model configuration after deletion: %w", err)
		}
		if doc.ConfigurationRevision < committedRevision {
			return fmt.Errorf("gatewayapp: canonical model configuration revision %d is older than committed revision %d", doc.ConfigurationRevision, committedRevision)
		}
		catalog, err := s.composition.canonicalModelLookup(doc)
		if err != nil {
			return err
		}
		placement := newPlacementSnapshot(doc)
		if err := controlplacement.ValidateSnapshot(placement.placement); err != nil {
			return fmt.Errorf("gatewayapp: validate placement after model removal: %w", err)
		}
		var fallback disconnectedACPFallback
		if len(removed.acp) > 0 {
			fallback, err = resolveDisconnectedACPFallback(placement, doc.ModelProfiles)
			if err != nil {
				return err
			}
		}
		missingACP := make(map[string]struct{}, len(removed.acp))
		for id := range removed.acp {
			if _, present := modelprofile.Lookup(doc.ModelProfiles, id); !present {
				missingACP[id] = struct{}{}
			}
		}
		var reconcileErr error
		if runtimes != nil {
			for _, runtime := range runtimes.snapshot() {
				if runtime == nil || runtime.instance == nil {
					continue
				}
				if err := runtime.instance.applyDeletedModelProfiles(reconcileCtx, placement, catalog, removed); err != nil {
					reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Session %q Runtime: %w", runtime.sessionID, err))
				}
			}
		}
		reconcileErr = errors.Join(reconcileErr, s.repairDeletedModelSessionSelections(reconcileCtx, runtimes, doc.ConfigurationRevision, catalog, removed, missingACP, fallback))
		observed, err := s.composition.authorities.store.LoadContext(reconcileCtx)
		if err != nil {
			return errors.Join(reconcileErr, fmt.Errorf("gatewayapp: verify canonical model configuration after deletion: %w", err))
		}
		if observed.ConfigurationRevision == doc.ConfigurationRevision {
			return reconcileErr
		}
	}
	return errors.New("gatewayapp: model removal reconciliation did not converge on a stable configuration revision")
}

func (s *controlCommandBackend) repairDeletedModelSessionSelections(
	ctx context.Context,
	runtimes *sessionRuntimeRegistry,
	revision uint64,
	catalog *modelLookup,
	removed removedModelProfiles,
	missingACP map[string]struct{},
	fallback disconnectedACPFallback,
) error {
	if s.composition.sessions == nil || s.modelRecovery == nil {
		return errors.New("gatewayapp: Session model reconciliation is unavailable")
	}
	var reconcileErr error
	cursor := ""
	for {
		listed, err := s.composition.sessions.ListSessions(ctx, session.ListSessionsRequest{
			AppName: s.composition.authorities.appName,
			UserID:  s.composition.authorities.userID,
			Cursor:  cursor,
			Limit:   100,
		})
		if err != nil {
			return errors.Join(reconcileErr, fmt.Errorf("gatewayapp: list Sessions for model reconciliation: %w", err))
		}
		for _, summary := range listed.Sessions {
			active, err := s.composition.sessions.Session(ctx, summary.SessionRef)
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Session %q state: %w", summary.SessionID, err))
				continue
			}
			closed, err := appserver.IsSessionClosed(ctx, s.composition.sessions, active.SessionRef)
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Session %q lifecycle: %w", summary.SessionID, err))
				continue
			}
			if closed {
				continue
			}
			if len(missingACP) > 0 {
				if err := s.reconcileDisconnectedACPBindings(ctx, runtimes, active, "", missingACP, fallback, revision); err != nil {
					reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Session %q ACP bindings: %w", summary.SessionID, err))
				}
			}
			if len(removed.providers) > 0 {
				if _, err := s.modelRecovery.repairSessionModelSelection(ctx, s.composition.sessions, active, catalog, removed.providerIDForSelector); err != nil {
					reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Session %q model selection: %w", summary.SessionID, err))
				}
			}
		}
		cursor = strings.TrimSpace(listed.NextCursor)
		if cursor == "" {
			return reconcileErr
		}
	}
}

func (s *runtimeComposition) applyDeletedModelProfiles(ctx context.Context, placement *placementSnapshot, canonical *modelLookup, removed removedModelProfiles) error {
	if s == nil || s.lookup == nil {
		return nil
	}
	s.placementCacheMu.Lock()
	s.placementCache = placement
	s.placementCacheGeneration++
	s.placementCacheMu.Unlock()
	if len(removed.providers) == 0 {
		return nil
	}
	canonicalDefault := canonical.DefaultID()
	_, defaultRetained := canonical.Config(s.lookup.DefaultID())
	replaceDefault := removed.hasProvider(s.lookup.DefaultID()) && !defaultRetained
	install := make([]ModelConfig, 0, len(removed.providers)+1)
	if canonicalDefault != "" && !s.lookup.HasAlias(canonicalDefault) {
		fallback, ok := canonical.Config(canonicalDefault)
		if !ok {
			return fmt.Errorf("canonical default model %q is unavailable", canonicalDefault)
		}
		install = append(install, fallback)
	}
	for _, modelID := range removed.providers {
		if configured, ok := canonical.Config(modelID); ok {
			install = append(install, configured)
		}
	}
	installed := map[string]struct{}{}
	for _, configured := range install {
		key := strings.ToLower(strings.TrimSpace(configured.ID))
		if _, ok := installed[key]; ok {
			continue
		}
		installed[key] = struct{}{}
		finishCredential, err := s.lookup.beginPinAPIKeyCredential(ctx, configured, canonical)
		if err != nil {
			return fmt.Errorf("pin canonical model %q credential: %w", configured.ID, err)
		}
		_, finishLookup, err := s.lookup.beginPinnedUpsert(configured)
		if err != nil {
			finishCredential(false)
			return fmt.Errorf("install canonical model %q: %w", configured.ID, err)
		}
		finishLookup(true)
		finishCredential(true)
	}
	s.lookup.mu.RLock()
	contextWindow := s.lookup.contextWindow
	s.lookup.mu.RUnlock()
	snapshot := s.lookup.Snapshot()
	remaining := snapshot.Configs[:0]
	for _, configured := range snapshot.Configs {
		if removed.hasProvider(configured.ID) {
			if _, present := canonical.Config(configured.ID); !present {
				continue
			}
		}
		remaining = append(remaining, configured)
	}
	snapshot.Configs = remaining
	if replaceDefault {
		snapshot.DefaultID = canonicalDefault
		snapshot.DefaultAlias = canonical.DefaultAlias()
		snapshot.DefaultEffort = canonical.DefaultEffort()
		snapshot.DefaultFastMode = canonical.DefaultFastMode()
	}
	s.lookup.Restore(snapshot, contextWindow)

	s.mu.Lock()
	if modelID := s.activeRuntime.Model.ID; removed.hasProvider(modelID) {
		configured, profileConfigured := s.lookup.Config(modelID)
		if !profileConfigured {
			configured, _ = s.lookup.Config(canonicalDefault)
		}
		s.activeRuntime.Model = configured
		s.activeRuntime.ModelProfileID = modelprofile.BuildProviderID(configured.ID)
		if !profileConfigured {
			s.activeRuntime.ModelProfileEffort = canonical.DefaultEffort()
			s.activeRuntime.ModelFastMode = canonical.DefaultFastMode()
		} else {
			if effort := modelcatalog.NormalizeReasoningEffort(s.activeRuntime.ModelProfileEffort); effort != "" && !modelConfigSupportsReasoningEffort(configured, effort) {
				s.activeRuntime.ModelProfileEffort = ""
			}
			if !modelconfig.SupportsSpeedMode(configured, "fast") {
				s.activeRuntime.ModelFastMode = false
			}
		}
	}
	gateway := s.gateway
	s.mu.Unlock()
	if gateway != nil && gateway.Resolver() != nil {
		gateway.Resolver().SetModelLookup(s.lookup, s.lookup.DefaultID())
	}
	return nil
}
