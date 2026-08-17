package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	modelprofilebuilder "github.com/caelis-labs/caelis/control/modelprofile/builder"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/discovery"
)

type acpPreparationEffectResult struct {
	Revision      uint64
	EffectStarted bool
	Preparation   controlagents.ACPPreparation
	Warning       error
}

func (*Stack) CanRecoverControlCommand(action appserver.Action) bool {
	return recoverableACPCommandAction(action) || recoverablePluginCommandAction(action)
}

func recoverableACPCommandAction(action appserver.Action) bool {
	switch action {
	case appserver.ActionACPAgentPrepare, appserver.ActionACPAgentPrepareAuth:
		return true
	default:
		return false
	}
}

// RecoverControlCommand proves terminal ACP preparation or plugin external-effect
// receipts from domain-owned durable records. It never repeats launcher,
// process, auth, install, marketplace fetch, Session, or configuration effects.
// Pure AppConfig mutations remain conservatively unknown when only an intent
// exists because their domain state is not operation-attributable.
func (s *Stack) RecoverControlCommand(
	ctx context.Context,
	principal appserver.Principal,
	intent appserver.OperationIntent,
	_ any,
) (appserver.CommandResult, bool, error) {
	if recoverablePluginCommandAction(intent.Action) {
		receipt, found, err := s.loadPluginOperationReceipt(ctx, principal.ID, intent.OperationID, intent.Digest)
		if err != nil || !found {
			return appserver.CommandResult{}, false, err
		}
		if receipt.Action != intent.Action || !receipt.Outcome.Valid() {
			return appserver.CommandResult{}, false, nil
		}
		return pluginCommandResultFromReceipt(receipt), true, nil
	}
	if !recoverableACPCommandAction(intent.Action) {
		return appserver.CommandResult{}, false, nil
	}
	if s == nil || s.acpPreparations == nil {
		return appserver.CommandResult{}, false, errors.New("gatewayapp: ACP preparation recovery is unavailable")
	}
	preparation, found, err := s.acpPreparations.FindByIntent(
		ctx,
		strings.TrimSpace(principal.ID),
		strings.TrimSpace(intent.OperationID),
		strings.TrimSpace(intent.Digest),
	)
	if err != nil || !found {
		return appserver.CommandResult{}, false, err
	}
	if preparation.State != controlagents.PreparationStateNeedsAuth && preparation.State != controlagents.PreparationStateReady {
		return appserver.CommandResult{}, false, nil
	}
	return acpPreparationCommandResult(acpPreparationEffectResult{
		Revision: preparation.ObservedRevision, Preparation: preparation,
	}), true, nil
}

func (s *Stack) prepareACPAtRevision(
	ctx context.Context,
	principal appserver.Principal,
	req appserver.PrepareACPRequest,
) (acpPreparationEffectResult, error) {
	result := acpPreparationEffectResult{Revision: expectedConfigurationRevision(req.ExpectedRevision)}
	if s == nil || s.composition.store == nil || s.acpPreparations == nil {
		return result, errors.New("gatewayapp: ACP preparation is unavailable")
	}
	prepared := controlagents.NormalizeACPPrepareRequest(req.Request)
	if err := validateACPPrepareLauncher(prepared); err != nil {
		return result, err
	}
	intent, ok := appserver.OperationIntentFromContext(ctx)
	if !ok || intent.Action != appserver.ActionACPAgentPrepare {
		return result, errors.New("gatewayapp: ACP prepare operation intent is unavailable")
	}
	if err := s.preflightACPPreparation(ctx, result.Revision); err != nil {
		return result, err
	}

	var connection controlagents.Connection
	var selectedAuthentication controlagents.Authentication
	if prepared.ParentRef != "" {
		loaded, err := s.ownedACPPreparation(ctx, principal.ID, prepared.ParentRef, "")
		if err != nil {
			return result, err
		}
		if loaded.State != controlagents.PreparationStateReady {
			return result, errors.New("gatewayapp: parent ACP preparation is not ready")
		}
		if loaded.Request.AdapterID != prepared.AdapterID || loaded.Request.Launcher != prepared.Launcher ||
			loaded.Request.CommandLine != prepared.CommandLine || loaded.Request.CWD != prepared.CWD {
			return result, errors.New("gatewayapp: parent ACP preparation belongs to another endpoint")
		}
		connection = loaded.Connection
		selectedAuthentication = loaded.SelectedAuthentication
	}

	planned, err := s.acpPreparations.CreatePlanned(ctx, controlagents.ACPPreparation{
		State:            controlagents.PreparationStatePlanned,
		PrincipalID:      strings.TrimSpace(principal.ID),
		OperationID:      intent.OperationID,
		IntentDigest:     intent.Digest,
		ParentRef:        prepared.ParentRef,
		Request:          prepared,
		Connection:       connection,
		ObservedRevision: result.Revision,
	})
	if err != nil && !configstore.WriteCommitted(err) {
		return result, err
	}
	result.Preparation = planned
	if err != nil {
		result.Warning = errors.Join(result.Warning, fmt.Errorf("gatewayapp: persist planned ACP preparation: %w", err))
	}

	if connection.ID == "" {
		if prepared.Launcher == controlagents.LauncherChoiceGlobal || prepared.Launcher == controlagents.LauncherChoiceManaged {
			result.EffectStarted = true
		}
		connection, err = s.resolveACPConnectionLauncher(ctx, connectRequestFromACPPrepare(prepared))
		if err != nil {
			return result, err
		}
		planned.Connection = connection
		planned, err = s.acpPreparations.Save(ctx, planned.ContentDigest, planned)
		if err != nil && !configstore.WriteCommitted(err) {
			return result, err
		}
		result.Preparation = planned
		if err != nil {
			result.Warning = errors.Join(result.Warning, fmt.Errorf("gatewayapp: persist resolved ACP preparation: %w", err))
		}
	}
	connection.Authentication = selectedAuthentication
	result.EffectStarted = true
	probe, err := s.acpDiscoveryService().Prepare(ctx, discovery.PrepareRequest{
		Connection:             connection,
		CWD:                    firstNonEmpty(prepared.CWD, s.composition.workspace.CWD),
		SelectedModelID:        prepared.ModelID,
		AuthenticationMethodID: selectedAuthentication.MethodID,
	})
	if err != nil || probe.State == discovery.PrepareUnknownCleanup {
		if err == nil {
			err = errors.New("gatewayapp: ACP preparation cleanup outcome cannot be proven")
		}
		planned.CleanupWarning = err.Error()
		if saved, saveErr := s.saveACPPreparationWarning(ctx, planned); saveErr == nil {
			result.Preparation = saved
		} else {
			if saved.Ref != "" {
				result.Preparation = saved
			}
			err = errors.Join(err, saveErr)
		}
		return result, err
	}

	terminal := planned
	terminal.Connection = connection
	terminal.CleanupWarning = ""
	switch probe.State {
	case discovery.PrepareNeedsAuth:
		terminal.State = controlagents.PreparationStateNeedsAuth
		terminal.AuthenticationMethods = preparationChallengeMethods(probe.AuthenticationMethods)
		terminal.SelectedAuthentication = controlagents.Authentication{}
		terminal.Connection.Authentication = controlagents.Authentication{}
	case discovery.PrepareReady:
		terminal.State = controlagents.PreparationStateReady
		terminal.Discovery = probe.Snapshot
		terminal.SelectedAuthentication = probe.Authentication
		terminal.Connection.Authentication = probe.Authentication
		if probe.Authentication.MethodID != "" {
			terminal.AuthenticationMethods = preparationChallengeMethods(probe.AuthenticationMethods)
		}
	default:
		return result, fmt.Errorf("gatewayapp: unsupported ACP prepare state %q", probe.State)
	}
	terminal, err = s.acpPreparations.Save(ctx, planned.ContentDigest, terminal)
	if err != nil && !configstore.WriteCommitted(err) {
		return result, err
	}
	result.Preparation = terminal
	if err != nil {
		result.Warning = errors.Join(result.Warning, fmt.Errorf("gatewayapp: persist terminal ACP preparation: %w", err))
	}
	return result, nil
}

func (s *Stack) prepareACPAuthenticationAtRevision(
	ctx context.Context,
	principal appserver.Principal,
	req appserver.PrepareACPAuthenticationRequest,
) (acpPreparationEffectResult, error) {
	result := acpPreparationEffectResult{Revision: expectedConfigurationRevision(req.ExpectedRevision)}
	if s == nil || s.acpPreparations == nil {
		return result, errors.New("gatewayapp: ACP preparation is unavailable")
	}
	intent, ok := appserver.OperationIntentFromContext(ctx)
	if !ok || intent.Action != appserver.ActionACPAgentPrepareAuth {
		return result, errors.New("gatewayapp: ACP prepare-auth operation intent is unavailable")
	}
	parent, err := s.ownedACPPreparation(ctx, principal.ID, req.PreparationRef, req.PreparationDigest)
	if err != nil {
		return result, err
	}
	if parent.State != controlagents.PreparationStateNeedsAuth {
		return result, errors.New("gatewayapp: ACP preparation does not need authentication")
	}
	method, ok := preparationChallengeMethod(parent.AuthenticationMethods, req.MethodID)
	if !ok {
		return result, errors.New("gatewayapp: selected ACP authentication method was not advertised")
	}
	if method.Type == controlagents.AuthenticationTerminal && !controlagents.TerminalAuthenticationAvailable(ctx) {
		return result, errorcode.New(errorcode.FailedPrecondition, "gatewayapp: terminal ACP authentication capability is unavailable")
	}
	if err := s.preflightACPPreparation(ctx, result.Revision); err != nil {
		return result, err
	}

	planned, err := s.acpPreparations.CreatePlanned(ctx, controlagents.ACPPreparation{
		State:            controlagents.PreparationStatePlanned,
		PrincipalID:      strings.TrimSpace(principal.ID),
		OperationID:      intent.OperationID,
		IntentDigest:     intent.Digest,
		ParentRef:        parent.Ref,
		Request:          parent.Request,
		Connection:       parent.Connection,
		ObservedRevision: result.Revision,
	})
	if err != nil && !configstore.WriteCommitted(err) {
		return result, err
	}
	result.Preparation = planned
	if err != nil {
		result.Warning = errors.Join(result.Warning, fmt.Errorf("gatewayapp: persist authenticated ACP preparation intent: %w", err))
	}
	result.EffectStarted = true
	probe, err := s.acpDiscoveryService().Prepare(ctx, discovery.PrepareRequest{
		Connection:             parent.Connection,
		CWD:                    firstNonEmpty(parent.Request.CWD, s.composition.workspace.CWD),
		SelectedModelID:        parent.Request.ModelID,
		AuthenticationMethodID: strings.TrimSpace(req.MethodID),
	})
	if err != nil || probe.State == discovery.PrepareUnknownCleanup {
		if err == nil {
			err = errors.New("gatewayapp: authenticated ACP preparation cleanup outcome cannot be proven")
		}
		planned.CleanupWarning = err.Error()
		if saved, saveErr := s.saveACPPreparationWarning(ctx, planned); saveErr == nil {
			result.Preparation = saved
		} else {
			if saved.Ref != "" {
				result.Preparation = saved
			}
			err = errors.Join(err, saveErr)
		}
		return result, err
	}
	if probe.State != discovery.PrepareReady {
		return result, errors.New("gatewayapp: explicit ACP authentication did not produce a ready preparation")
	}
	ready := planned
	ready.State = controlagents.PreparationStateReady
	ready.Discovery = probe.Snapshot
	ready.AuthenticationMethods = parent.AuthenticationMethods
	ready.SelectedAuthentication = probe.Authentication
	ready.Connection.Authentication = probe.Authentication
	ready, err = s.acpPreparations.Save(ctx, planned.ContentDigest, ready)
	if err != nil && !configstore.WriteCommitted(err) {
		return result, err
	}
	result.Preparation = ready
	if err != nil {
		result.Warning = errors.Join(result.Warning, fmt.Errorf("gatewayapp: persist authenticated ACP preparation: %w", err))
	}
	return result, nil
}

func (s *Stack) connectPreparedACPAtRevision(
	ctx context.Context,
	principal appserver.Principal,
	req appserver.ConnectACPRequest,
) (externalAgentMutationResult, modelprofile.ModelProfile, error) {
	mutation := externalAgentMutationResult{Revision: expectedConfigurationRevision(req.ExpectedRevision)}
	preparation, err := s.ownedACPPreparation(ctx, principal.ID, req.PreparationRef, req.PreparationDigest)
	if err != nil {
		return mutation, modelprofile.ModelProfile{}, err
	}
	if preparation.State != controlagents.PreparationStateReady {
		return mutation, modelprofile.ModelProfile{}, errors.New("gatewayapp: ACP preparation is not ready")
	}
	model, defaults, err := controlagents.ResolveDiscoverySelection(
		preparation.Discovery,
		preparation.Request.ModelID,
		req.ConfigValues,
	)
	if err != nil {
		return mutation, modelprofile.ModelProfile{}, fmt.Errorf("gatewayapp: %w", err)
	}

	doc, err := s.composition.store.LoadContext(ctx)
	if err != nil {
		return mutation, modelprofile.ModelProfile{}, err
	}
	mutation.Revision = doc.ConfigurationRevision
	expected := expectedConfigurationRevision(req.ExpectedRevision)
	if doc.ConfigurationRevision != expected {
		return mutation, modelprofile.ModelProfile{}, &configstore.ConfigurationRevisionConflict{Expected: expected, Actual: doc.ConfigurationRevision}
	}
	next, agent, err := controlagents.UpsertExternalConnection(
		doc.ExternalAgents,
		preparation.Connection,
		preparation.Discovery,
		externalAgentNameAllowed,
	)
	if err != nil {
		return mutation, modelprofile.ModelProfile{}, fmt.Errorf("gatewayapp: update external Agent configuration: %w", err)
	}
	profile, err := modelprofilebuilder.FromACP(agent, preparation.Connection, model, defaults, preparation.Discovery)
	if err != nil {
		return mutation, modelprofile.ModelProfile{}, fmt.Errorf("gatewayapp: build ACP model profile: %w", err)
	}
	doc.ExternalAgents = next
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, profile)
	if err != nil {
		return mutation, modelprofile.ModelProfile{}, fmt.Errorf("gatewayapp: update model profile catalog: %w", err)
	}
	saved, persistErr := s.composition.store.CompareAndSave(ctx, expected, doc)
	if persistErr != nil && !configstore.WriteCommitted(persistErr) {
		mutation.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
		return mutation, modelprofile.ModelProfile{}, persistErr
	}
	mutation.EffectStarted = true
	mutation.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
	if saved.ConfigurationRevision == 0 {
		reconcileErr := s.reconcileCommittedExternalAgents(ctx)
		return mutation, profile, errors.Join(
			persistErr,
			errors.New("gatewayapp: committed external Agent configuration revision is unknown"),
			wrapOptionalError("gatewayapp: reconcile unobserved external Agent configuration", reconcileErr),
		)
	}
	mutation.Warning = wrapOptionalError("gatewayapp: external Agent configuration durability warning", persistErr)
	if preparation.Connection.Launcher.Kind == controlagents.LaunchKindManaged &&
		pathWithinRoot(preparation.Connection.Launcher.Command, filepath.Join(s.managedACPAgentRoot(), "installations")) {
		s.cleanupLegacyManagedACPInstallIfUnused()
	}
	return mutation, profile, nil
}

func (s *Stack) preflightACPPreparation(ctx context.Context, expected uint64) error {
	doc, err := s.composition.store.LoadContext(ctx)
	if err != nil {
		return err
	}
	if doc.ConfigurationRevision != expected {
		return &configstore.ConfigurationRevisionConflict{Expected: expected, Actual: doc.ConfigurationRevision}
	}
	return nil
}

// ACPPreparationReadService is the focused Host-private preparation read
// authority used by the AppServer Agent capability.
type ACPPreparationReadService struct {
	store *acpPreparationStore
}

// ACPPreparationReads returns the focused preparation read authority.
func (s *Stack) ACPPreparationReads() ACPPreparationReadService {
	if s == nil {
		return ACPPreparationReadService{}
	}
	return ACPPreparationReadService{store: s.acpPreparations}
}

// Preparation loads one preparation owned by the bound principal.
func (s ACPPreparationReadService) Preparation(ctx context.Context, principalID string, ref string) (controlagents.ACPPreparation, error) {
	preparation, err := ownedACPPreparation(s.store, ctx, principalID, ref, "")
	switch {
	case err == nil:
		return preparation, nil
	case errors.Is(err, errACPPreparationNotFound), errors.Is(err, errACPPreparationExpired):
		return controlagents.ACPPreparation{}, errorcode.Wrap(errorcode.NotFound, "gatewayapp: ACP preparation is unavailable", err)
	case errors.Is(err, errACPPreparationInvalidRef):
		return controlagents.ACPPreparation{}, errorcode.Wrap(errorcode.InvalidArgument, "gatewayapp: invalid ACP preparation reference", err)
	default:
		return controlagents.ACPPreparation{}, err
	}
}

func (s *Stack) ownedACPPreparation(ctx context.Context, principalID, ref, digest string) (controlagents.ACPPreparation, error) {
	if s == nil {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation is unavailable")
	}
	return ownedACPPreparation(s.acpPreparations, ctx, principalID, ref, digest)
}

func ownedACPPreparation(store *acpPreparationStore, ctx context.Context, principalID, ref, digest string) (controlagents.ACPPreparation, error) {
	if store == nil {
		return controlagents.ACPPreparation{}, errors.New("gatewayapp: ACP preparation is unavailable")
	}
	preparation, err := store.Get(ctx, ref)
	if err != nil {
		return controlagents.ACPPreparation{}, err
	}
	if preparation.PrincipalID != strings.TrimSpace(principalID) {
		return controlagents.ACPPreparation{}, appserver.ErrUnauthorized
	}
	if digest = strings.ToLower(strings.TrimSpace(digest)); digest != "" && preparation.ContentDigest != digest {
		return controlagents.ACPPreparation{}, errACPPreparationConflict
	}
	return preparation, nil
}

func validateACPPrepareLauncher(request controlagents.ACPPrepareRequest) error {
	request = controlagents.NormalizeACPPrepareRequest(request)
	if request.AdapterID == "" {
		return errors.New("gatewayapp: ACP adapter is required")
	}
	if _, ok := agentregistry.LookupConnectableAgent(request.AdapterID); !ok {
		return fmt.Errorf("gatewayapp: unknown ACP adapter %q", request.AdapterID)
	}
	if !agentregistry.SupportsLauncher(request.AdapterID, request.Launcher) {
		return fmt.Errorf("gatewayapp: ACP adapter %q does not support launcher %q", request.AdapterID, request.Launcher)
	}
	if request.Launcher == controlagents.LauncherChoiceCommand {
		if _, _, err := splitACPCommandLine(request.CommandLine); err != nil {
			return err
		}
	}
	return nil
}

func connectRequestFromACPPrepare(request controlagents.ACPPrepareRequest) controlagents.ConnectRequest {
	request = controlagents.NormalizeACPPrepareRequest(request)
	return controlagents.ConnectRequest{
		AdapterID: request.AdapterID, Launcher: request.Launcher,
		CommandLine: request.CommandLine, ModelID: request.ModelID, CWD: request.CWD,
	}
}

func preparationChallengeMethods(methods []controlagents.AuthenticationMethod) []controlagents.AuthenticationChallengeMethod {
	result := make([]controlagents.AuthenticationChallengeMethod, 0, len(methods))
	for _, method := range controlagents.CloneAuthenticationMethods(methods) {
		result = append(result, controlagents.AuthenticationChallengeMethod{
			ID: method.ID, Name: method.Name, Description: method.Description, Type: method.Type,
		})
	}
	return result
}

func preparationChallengeMethod(methods []controlagents.AuthenticationChallengeMethod, id string) (controlagents.AuthenticationChallengeMethod, bool) {
	id = strings.TrimSpace(id)
	for _, method := range methods {
		if strings.TrimSpace(method.ID) == id {
			return method, true
		}
	}
	return controlagents.AuthenticationChallengeMethod{}, false
}

func (s *Stack) saveACPPreparationWarning(ctx context.Context, preparation controlagents.ACPPreparation) (controlagents.ACPPreparation, error) {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(contextOrBackground(ctx)), 5*time.Second)
	defer cancel()
	return s.acpPreparations.Save(saveCtx, preparation.ContentDigest, preparation)
}
