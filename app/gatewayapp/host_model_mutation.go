package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
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

func (s *Stack) connectModelsAtRevision(ctx context.Context, configs []ModelConfig, expected *uint64) (result hostModelMutationResult, returnErr error) {
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

	previousDefaultID := strings.TrimSpace(candidate.DefaultID())
	_, hadDefault := candidate.Config(previousDefaultID)
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
	if !hadDefault {
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

func (s *Stack) useHostModelAtRevision(ctx context.Context, alias string, reasoningEffort string, expected *uint64) (result hostModelMutationResult, returnErr error) {
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
	cfg, err := candidate.ResolveConfig(alias)
	if err != nil {
		return result, err
	}
	reasoning := strings.TrimSpace(reasoningEffort)
	if reasoning != "" && !modelConfigSupportsReasoningEffort(cfg, reasoning) {
		return result, fmt.Errorf("gatewayapp: model %q does not support reasoning level %q", alias, reasoning)
	}
	doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, modelprofile.BuildProviderID(cfg.ID), reasoning)
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

func (s *Stack) deleteHostModelAtRevision(ctx context.Context, alias string, expected *uint64) (result hostModelMutationResult, returnErr error) {
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
	cfg, err := candidate.ResolveConfig(alias)
	if err != nil {
		return result, err
	}
	profileID := modelprofile.BuildProviderID(cfg.ID)
	doc.AgentBindings, err = agentbinding.PrepareProfileRemoval(doc.AgentBindings, profileID)
	if err != nil {
		return result, err
	}
	doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, profileID)
	if err := candidate.Delete(alias); err != nil {
		return result, err
	}
	doc.ModelProfiles, err = modelprofile.SelectDefault(
		doc.ModelProfiles,
		modelprofile.BuildProviderID(candidate.DefaultID()),
		doc.ModelProfiles.DefaultEffort,
	)
	if err != nil {
		return result, err
	}
	if candidate.DefaultID() != "" {
		candidate.SetDefault(candidate.DefaultID(), doc.ModelProfiles.DefaultEffort)
	}
	doc.Models = candidate.Snapshot()

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

func (s *Stack) loadModelConfigurationCandidate(ctx context.Context, expected *uint64) (AppConfig, *modelLookup, error) {
	if s == nil || s.composition.authorities.store == nil {
		return AppConfig{}, nil, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if s.composition.lookup == nil {
		return AppConfig{}, nil, fmt.Errorf("gatewayapp: model lookup unavailable")
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

func (s *Stack) persistModelConfiguration(ctx context.Context, doc AppConfig) (AppConfig, error, error) {
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
// a CAS commit. It never rebuilds a Runtime; activated Sessions keep their
// detached model and Agent assembly until release.
func (s *Stack) observeCommittedModelConfiguration(ctx context.Context, committed AppConfig) error {
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
	s.composition.setRuntimeDefaultModelFromLookup()
	if gw := s.composition.currentGateway(); gw != nil && gw.Resolver() != nil {
		gw.Resolver().SetModelLookup(s.composition.lookup, s.composition.lookup.DefaultID())
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
