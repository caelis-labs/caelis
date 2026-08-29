package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
	modelprofilebuilder "github.com/caelis-labs/caelis/control/modelprofile/builder"
)

// hostModelMutationResult separates the durable configuration receipt from
// post-commit observation warnings. EffectStarted is used only to choose a
// conservative recoverable outcome when the durable effect cannot be proven.
type hostModelMutationResult struct {
	Revision      uint64
	EffectStarted bool
	Warning       error
}

func (s *controlCommandBackend) connectModelsAtRevision(ctx context.Context, configs []ModelConfig, expected *uint64) (result hostModelMutationResult, returnErr error) {
	if s == nil {
		return result, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if len(configs) == 0 {
		return result, fmt.Errorf("gatewayapp: at least one model config is required")
	}
	doc, candidate, err := s.loadModelConfigurationCandidate(ctx, expected)
	if err != nil {
		result.Revision = configurationErrorRevision(err, doc.ConfigurationRevision)
		return result, err
	}
	result.Revision = doc.ConfigurationRevision

	prepared, credentialTxn, err := s.prepareProviderCredentials(ctx, configs)
	if err != nil {
		if errors.Is(err, credentialstore.ErrRollbackIncomplete) {
			result.EffectStarted = true
		}
		return result, rollbackModelCredentials(credentialTxn, err, &result)
	}
	defer func() {
		if returnErr != nil {
			returnErr = rollbackModelCredentials(credentialTxn, returnErr, &result)
		}
	}()

	modelIDs := make([]string, 0, len(prepared))
	for _, cfg := range prepared {
		modelID, upsertErr := candidate.upsert(cfg, false)
		if upsertErr != nil {
			return result, fmt.Errorf("gatewayapp: invalid model config: %w", upsertErr)
		}
		modelIDs = append(modelIDs, modelID)
	}

	profiles := make([]modelprofile.ModelProfile, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		configured, ok := candidate.Config(modelID)
		if !ok {
			return result, fmt.Errorf("gatewayapp: connected model %q is unavailable", modelID)
		}
		profile, profileErr := modelprofilebuilder.FromProvider(configured)
		if profileErr != nil {
			return result, fmt.Errorf("gatewayapp: build model profile for %q: %w", modelID, profileErr)
		}
		profiles = append(profiles, profile)
	}
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, profiles...)
	if err != nil {
		return result, fmt.Errorf("gatewayapp: update model profile catalog: %w", err)
	}
	// Provider lookup omits ACP defaults, so a still-valid unified default
	// (ACP or provider) must be preserved instead of inferred from DefaultID.
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, doc.ModelProfiles.DefaultProfileID); !ok {
		doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, profiles[0].ID, "")
		if err != nil {
			return result, fmt.Errorf("gatewayapp: select default model profile: %w", err)
		}
		candidate.SetDefault(modelIDs[0], doc.ModelProfiles.DefaultEffort)
	}
	doc.Models = candidate.Snapshot()

	saved, persistWarning, persistErr := s.persistModelConfiguration(ctx, doc)
	if persistErr != nil {
		result.EffectStarted = configstore.WriteCommitted(persistErr)
		result.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
		if result.EffectStarted {
			// The canonical document may already reference the stable endpoint
			// credential. Accept the replacement before releasing its lock and
			// reconcile live state only from a fresh canonical read. If that read
			// remains unavailable, keep the receipt unknown and leave live state
			// unchanged instead of installing an unobserved candidate.
			commitErr := credentialTxn.commit()
			installErr := s.observeCommittedModelConfiguration(ctx, saved)
			return result, errors.Join(
				persistErr,
				wrapOptionalError("gatewayapp: commit provider credentials", commitErr),
				wrapOptionalError("gatewayapp: roll forward unobserved model configuration", installErr),
			)
		}
		return result, persistErr
	}
	credentialCommitWarning := wrapOptionalError("gatewayapp: commit provider credentials", credentialTxn.commit())
	result.EffectStarted = true
	result.Revision = saved.ConfigurationRevision
	result.Warning = errors.Join(persistWarning, credentialCommitWarning, s.observeCommittedModelConfiguration(ctx, saved))
	return result, nil
}

func (s *controlCommandBackend) useHostModelAtRevision(ctx context.Context, alias string, reasoningEffort string, expected *uint64) (result hostModelMutationResult, returnErr error) {
	if s == nil {
		return result, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return result, fmt.Errorf("gatewayapp: model alias is required")
	}
	doc, candidate, err := s.loadModelConfigurationCandidate(ctx, expected)
	if err != nil {
		result.Revision = configurationErrorRevision(err, doc.ConfigurationRevision)
		return result, err
	}
	result.Revision = doc.ConfigurationRevision
	reasoning := strings.TrimSpace(reasoningEffort)
	profile, profileSelected := modelprofile.Lookup(doc.ModelProfiles, alias)
	if !profileSelected {
		cfg, resolveErr := candidate.ResolveConfig(alias)
		if resolveErr != nil {
			return result, errorcode.Wrap(errorcode.InvalidArgument, resolveErr.Error(), resolveErr)
		}
		profile, profileSelected = modelprofile.Lookup(doc.ModelProfiles, modelprofile.BuildProviderID(cfg.ID))
		if !profileSelected {
			return result, errorcode.New(errorcode.InvalidArgument, fmt.Sprintf("gatewayapp: model %q has no selectable profile", alias))
		}
	}
	if reasoning == "" {
		reasoning = profile.Effort.DefaultEffort
	}
	if !profile.SupportsEffort(reasoning) {
		return result, errorcode.New(errorcode.InvalidArgument, fmt.Sprintf("gatewayapp: model profile %q does not support reasoning level %q", profile.ID, reasoning))
	}
	doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, profile.ID, reasoning)
	if err != nil {
		return result, err
	}

	saved, persistWarning, persistErr := s.persistModelConfiguration(ctx, doc)
	if persistErr != nil {
		result.EffectStarted = configstore.WriteCommitted(persistErr)
		result.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
		if result.EffectStarted {
			persistErr = errors.Join(
				persistErr,
				wrapOptionalError("gatewayapp: roll forward unobserved model configuration", s.observeCommittedModelConfiguration(ctx, saved)),
			)
		}
		return result, persistErr
	}
	result.EffectStarted = true
	result.Revision = saved.ConfigurationRevision
	result.Warning = errors.Join(persistWarning, s.observeCommittedModelConfiguration(ctx, saved))
	return result, nil
}

func (s *controlCommandBackend) deleteHostModelAtRevision(ctx context.Context, alias string, expected *uint64) (result hostModelMutationResult, returnErr error) {
	if s == nil {
		return result, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return result, fmt.Errorf("gatewayapp: model alias is required")
	}
	doc, candidate, err := s.loadModelConfigurationCandidate(ctx, expected)
	if err != nil {
		result.Revision = configurationErrorRevision(err, doc.ConfigurationRevision)
		return result, err
	}
	result.Revision = doc.ConfigurationRevision
	beforeDoc := doc
	cfg, err := candidate.ResolveConfig(alias)
	if err != nil {
		return result, err
	}
	profileID := modelprofile.BuildProviderID(cfg.ID)
	deletedDefault := modelprofile.NormalizeID(doc.ModelProfiles.DefaultProfileID) == profileID
	doc.AgentBindings, _ = agentbinding.RemoveProfileBindings(doc.AgentBindings, profileID)
	doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, profileID)
	if err := candidate.Delete(alias); err != nil {
		return result, err
	}
	remaining := modelprofile.NormalizeConfiguration(doc.ModelProfiles)
	if deletedDefault && remaining.DefaultProfileID == "" && len(remaining.Profiles) > 0 {
		doc.ModelProfiles, err = modelprofile.SelectDefault(remaining, remaining.Profiles[0].ID, "")
		if err != nil {
			return result, err
		}
	}
	if candidate.DefaultID() != "" {
		candidate.SetDefault(candidate.DefaultID(), doc.ModelProfiles.DefaultEffort)
	}
	doc.Models = candidate.Snapshot()

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
				wrapOptionalError("gatewayapp: roll forward unobserved model configuration", s.observeCommittedModelConfiguration(ctx, saved)),
				wrapOptionalError("gatewayapp: reconcile active Session model removal", s.reconcileDeletedModelProfile(ctx, cfg.ID)),
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
		s.reconcileDeletedModelProfile(ctx, cfg.ID),
	)
	return result, nil
}

func (s *controlCommandBackend) loadModelConfigurationCandidate(ctx context.Context, expected *uint64) (AppConfig, *modelLookup, error) {
	if s == nil || s.composition.authorities.store == nil {
		return AppConfig{}, nil, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if s.composition.lookup == nil {
		return AppConfig{}, nil, fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	if err := recoverProviderCredentialRetirements(
		ctx,
		s.composition.authorities.store,
		s.composition.authorities.apiKeyCredentials,
	); err != nil {
		return AppConfig{}, nil, err
	}
	doc, err := s.composition.authorities.store.LoadContext(contextOrBackground(ctx))
	if err != nil {
		return AppConfig{}, nil, err
	}
	if expected != nil && doc.ConfigurationRevision != *expected {
		return doc, nil, &configstore.ConfigurationRevisionConflict{Expected: *expected, Actual: doc.ConfigurationRevision}
	}
	s.composition.lookup.mu.RLock()
	contextWindow := s.composition.lookup.contextWindow
	s.composition.lookup.mu.RUnlock()
	candidate, err := newModelLookupFromDocument(doc, contextWindow)
	if err != nil {
		return doc, nil, err
	}
	return doc, candidate, nil
}

func (s *controlCommandBackend) persistModelConfiguration(ctx context.Context, doc AppConfig) (AppConfig, error, error) {
	saved, err := s.composition.authorities.store.CompareAndSave(contextOrBackground(ctx), doc.ConfigurationRevision, doc)
	if err != nil && !configstore.WriteCommitted(err) {
		return saved, nil, err
	}
	if saved.ConfigurationRevision == 0 {
		return saved, nil, errors.Join(err, errors.New("gatewayapp: committed model configuration revision is unknown"))
	}
	return saved, wrapOptionalError("gatewayapp: model configuration durability warning", err), nil
}

// observeCommittedModelConfiguration refreshes Host catalog/status state after
// a CAS commit. Ordinary additions and default changes do not rebuild activated
// Session Runtimes; explicit profile deletion is reconciled separately below.
func (s *controlCommandBackend) observeCommittedModelConfiguration(ctx context.Context, committed AppConfig) error {
	if s == nil || s.composition.lookup == nil || s.composition.authorities.store == nil {
		return errors.New("gatewayapp: model lookup unavailable after configuration commit")
	}
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(contextOrBackground(ctx)), 5*time.Second)
	defer cancel()
	doc, err := s.composition.authorities.store.LoadContext(reconcileCtx)
	if err != nil {
		return fmt.Errorf("gatewayapp: observe canonical model configuration after commit: %w", err)
	}
	if committed.ConfigurationRevision > 0 && doc.ConfigurationRevision < committed.ConfigurationRevision {
		return fmt.Errorf(
			"gatewayapp: canonical model configuration revision %d is older than committed revision %d",
			doc.ConfigurationRevision,
			committed.ConfigurationRevision,
		)
	}
	s.composition.lookup.mu.RLock()
	contextWindow := s.composition.lookup.contextWindow
	s.composition.lookup.mu.RUnlock()
	candidate, err := newModelLookupFromDocument(doc, contextWindow)
	if err != nil {
		return fmt.Errorf("gatewayapp: rebuild committed model lookup: %w", err)
	}
	s.composition.lookup.Restore(candidate.Snapshot(), candidate.contextWindow)
	if pins := s.composition.authorities.sessionModelPins; pins != nil {
		pins.syncConfiguredModels(candidate.Snapshot().Configs)
	}
	s.composition.invalidateOwnPlacementSnapshot()
	if _, err := s.composition.placementSnapshot(reconcileCtx); err != nil {
		return fmt.Errorf("gatewayapp: refresh committed model profile catalog: %w", err)
	}
	s.composition.setRuntimeDefaultProfile(doc.ModelProfiles)
	if gw := s.composition.currentGateway(); gw != nil && gw.Resolver() != nil {
		gw.Resolver().SetModelLookup(s.composition.lookup, s.composition.lookup.DefaultID())
	}
	return nil
}

// reconcileDeletedModelProfile removes one committed provider profile from
// every live Session execution snapshot and repairs each affected durable
// Session selection to the canonical fallback. In-flight work may finish with
// values it already resolved, but later Turns and Spawn placement cannot keep
// using a profile that no longer exists.
func (s *controlCommandBackend) reconcileDeletedModelProfile(ctx context.Context, modelID string) error {
	if s == nil || s.composition.authorities.store == nil {
		return errors.New("gatewayapp: model configuration is unavailable after deletion")
	}
	// The provider removal is already committed. Complete revocation for every
	// live and durable Session even when the initiating client disconnects; a
	// bounded best-effort pass could otherwise leave a loaded Runtime presenting
	// or resolving the deleted model indefinitely.
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
		current, err := newModelLookupFromDocument(doc, 0)
		if err != nil {
			return fmt.Errorf("gatewayapp: rebuild canonical model catalog after deletion: %w", err)
		}
		if pins := s.composition.authorities.sessionModelPins; pins != nil {
			pins.syncConfiguredModels(current.Snapshot().Configs)
		}
		var reconcileErr error
		if runtimes != nil {
			for _, runtime := range runtimes.snapshot() {
				if runtime == nil || runtime.instance == nil {
					continue
				}
				if err := runtime.instance.applyDeletedModelProfile(reconcileCtx, doc, modelID); err != nil {
					reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Session %q Runtime: %w", runtime.sessionID, err))
					continue
				}
			}
		}
		reconcileErr = errors.Join(reconcileErr, s.repairDeletedModelSessionSelections(reconcileCtx))
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

func (s *controlCommandBackend) repairDeletedModelSessionSelections(ctx context.Context) error {
	if s == nil || s.composition == nil || s.composition.sessions == nil || s.modelRecovery == nil {
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
			if _, err := s.modelRecovery.repairMissingSessionModelSelection(ctx, s.composition.sessions, active); err != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Session %q model selection: %w", summary.SessionID, err))
			}
		}
		cursor = strings.TrimSpace(listed.NextCursor)
		if cursor == "" {
			return reconcileErr
		}
	}
}

func (s *runtimeComposition) applyDeletedModelProfile(ctx context.Context, doc AppConfig, modelID string) error {
	if s == nil || s.lookup == nil {
		return nil
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}

	s.lookup.mu.RLock()
	contextWindow := s.lookup.contextWindow
	s.lookup.mu.RUnlock()
	canonical, err := newModelLookupFromDocument(doc, contextWindow)
	if err != nil {
		return err
	}
	if s.authorities.apiKeyCredentials != nil {
		canonical.resolveAPIKey = s.authorities.apiKeyCredentials.Get
	}
	canonicalConfig, profileConfigured := canonical.Config(modelID)
	canonicalDefault := strings.TrimSpace(canonical.DefaultID())
	install := make([]ModelConfig, 0, 2)
	if canonicalDefault != "" && !s.lookup.HasAlias(canonicalDefault) {
		fallback, ok := canonical.Config(canonicalDefault)
		if !ok {
			return fmt.Errorf("canonical default model %q is unavailable", canonicalDefault)
		}
		install = append(install, fallback)
	}
	if profileConfigured {
		install = append(install, canonicalConfig)
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
	contextWindow = s.lookup.contextWindow
	s.lookup.mu.RUnlock()
	snapshot := s.lookup.Snapshot()
	remaining := snapshot.Configs[:0]
	for _, configured := range snapshot.Configs {
		if !profileConfigured && strings.EqualFold(strings.TrimSpace(configured.ID), modelID) {
			continue
		}
		remaining = append(remaining, configured)
	}
	snapshot.Configs = remaining
	snapshot.DefaultAlias = ""
	snapshot.DefaultID = ""
	snapshot.DefaultEffort = ""
	for _, configured := range snapshot.Configs {
		if strings.EqualFold(strings.TrimSpace(configured.ID), canonicalDefault) {
			snapshot.DefaultID = configured.ID
			snapshot.DefaultAlias = configured.Alias
			snapshot.DefaultEffort = canonical.DefaultEffort()
			break
		}
	}
	s.lookup.Restore(snapshot, contextWindow)

	nextPlacement := newPlacementSnapshot(doc)
	s.placementCacheMu.Lock()
	s.placementCache = nextPlacement
	s.placementCacheGeneration++
	s.placementCacheMu.Unlock()

	s.mu.Lock()
	if strings.EqualFold(strings.TrimSpace(s.activeRuntime.Model.ID), modelID) {
		configured, _ := s.lookup.Config(modelID)
		if !profileConfigured {
			configured, _ = s.lookup.Config(s.lookup.DefaultID())
		}
		s.activeRuntime.Model = configured
		s.activeRuntime.ModelProfileID = modelprofile.BuildProviderID(configured.ID)
		if !profileConfigured {
			s.activeRuntime.ModelProfileEffort = s.lookup.DefaultEffort()
		} else if effort := modelcatalog.NormalizeReasoningEffort(s.activeRuntime.ModelProfileEffort); effort != "" && !modelConfigSupportsReasoningEffort(configured, effort) {
			s.activeRuntime.ModelProfileEffort = ""
		}
	}
	gateway := s.gateway
	s.mu.Unlock()
	if gateway != nil && gateway.Resolver() != nil {
		gateway.Resolver().SetModelLookup(s.lookup, s.lookup.DefaultID())
	}
	return nil
}

func rollbackModelCredentials(txn *providerCredentialTransaction, cause error, result *hostModelMutationResult) error {
	rollbackErr := txn.rollback()
	if rollbackErr == nil {
		return cause
	}
	if result != nil {
		result.EffectStarted = true
	}
	return errors.Join(cause, fmt.Errorf("gatewayapp: rollback provider credentials: %w", rollbackErr))
}

func configurationErrorRevision(err error, fallback uint64) uint64 {
	var conflict *configstore.ConfigurationRevisionConflict
	if errors.As(err, &conflict) {
		return conflict.Actual
	}
	return fallback
}
