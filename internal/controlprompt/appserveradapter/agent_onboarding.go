package appserveradapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func (a *SessionClientAdapter) DiscoverACPConnection(ctx context.Context, request controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	if a == nil || a.agentClient == nil || a.statusClient == nil {
		return controlagents.DiscoverySnapshot{}, errors.New("app/gatewayapp/controladapter: ACP preparation client is unavailable")
	}
	a.acpPreparationMu.Lock()
	defer a.acpPreparationMu.Unlock()
	if len(request.ModelIDs) > 0 {
		return controlagents.DiscoverySnapshot{}, errors.New("ACP discovery addresses one model at a time through ModelID")
	}
	preparation, err := a.prepareACPConnectionLocked(ctx, request, "")
	return preparation.Discovery, err
}

func (a *SessionClientAdapter) ConnectACP(ctx context.Context, request controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
	if a == nil || a.agentClient == nil || a.statusClient == nil {
		return controlagents.ConnectResult{}, errors.New("app/gatewayapp/controladapter: ACP connection client is unavailable")
	}
	a.acpPreparationMu.Lock()
	defer a.acpPreparationMu.Unlock()
	request = controlagents.NormalizeConnectRequest(request)
	if strings.TrimSpace(request.CWD) == "" {
		request.CWD = a.WorkspaceDir()
	}
	if len(request.ModelIDs) > 0 && (request.ModelID != "" || len(request.ConfigValues) > 0) {
		return controlagents.ConnectResult{}, errors.New("ACP ModelIDs cannot be used with ModelID or ConfigValues")
	}
	preparation, err := a.prepareACPConnectionLocked(ctx, request, "")
	if err != nil {
		return controlagents.ConnectResult{}, err
	}
	var modelPreparations []appserver.ACPModelPreparationRef
	if len(request.ModelIDs) > 0 {
		modelPreparations, err = a.prepareACPConnectModelsLocked(ctx, request, preparation)
		if err != nil {
			return controlagents.ConnectResult{}, err
		}
	}
	before, err := a.addressedStatus(ctx, "", false)
	if err != nil {
		return controlagents.ConnectResult{}, fmt.Errorf("app/gatewayapp/controladapter: read Host revision before ACP connect: %w", err)
	}
	revision := before.Configuration.Revision
	receipt, commandErr := a.agentClient.ConnectACP(ctx, appserver.ConnectACPRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "agent-acp-connect-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		PreparationRef: preparation.Ref, PreparationDigest: preparation.ContentDigest,
		ConfigValues: request.ConfigValues, ModelPreparations: modelPreparations,
	})
	if receipt.Outcome != appserver.OutcomeCommitted {
		if commandErr == nil {
			commandErr = fmt.Errorf("ACP connect outcome is %q: %s", receipt.Outcome, strings.TrimSpace(receipt.Detail))
		}
		return controlagents.ConnectResult{}, &appserver.CommandReceiptError{Receipt: receipt, Err: commandErr}
	}
	profiles, err := acpConnectedProfiles(receipt, preparation, len(modelPreparations))
	if err != nil {
		return controlagents.ConnectResult{}, &appserver.CommandReceiptError{Receipt: receipt, Err: errors.Join(commandErr, err)}
	}
	connected := controlagents.ConnectResult{Connection: preparation.Connection, Discovery: preparation.Discovery, Profiles: profiles}
	if commandErr != nil || strings.TrimSpace(receipt.Detail) != "" {
		warning := commandErr
		if detail := strings.TrimSpace(receipt.Detail); detail != "" {
			warning = errors.Join(warning, fmt.Errorf(
				"app/gatewayapp/controladapter: ACP connect committed as operation %q with a warning; do not retry blindly: %s",
				receipt.OperationID, detail,
			))
		}
		return connected, &appserver.CommandReceiptError{Receipt: receipt, Err: warning}
	}
	return connected, nil
}

func (a *SessionClientAdapter) prepareACPConnectModelsLocked(ctx context.Context, request controlagents.ConnectRequest, roster controlagents.ACPPreparation) ([]appserver.ACPModelPreparationRef, error) {
	seen := make(map[string]bool, len(request.ModelIDs))
	for _, modelID := range request.ModelIDs {
		if controlagents.IsDefaultRemoteModelID(modelID) {
			return nil, errors.New("use ModelID to connect Agent default")
		}
		if seen[modelID] {
			return nil, fmt.Errorf("ACP model %q was selected more than once", modelID)
		}
		if _, _, err := controlagents.ResolveDiscoverySelection(roster.Discovery, modelID, nil); err != nil {
			return nil, err
		}
		seen[modelID] = true
	}
	refs := make([]appserver.ACPModelPreparationRef, 0, len(request.ModelIDs))
	selection := request
	selection.ModelIDs = nil
	for _, modelID := range request.ModelIDs {
		selection.ModelID = modelID
		prepared, err := a.prepareACPConnectionLocked(ctx, selection, roster.Ref)
		if err != nil {
			return nil, fmt.Errorf("prepare ACP model %q: %w", modelID, err)
		}
		refs = append(refs, appserver.ACPModelPreparationRef{Ref: prepared.Ref, Digest: prepared.ContentDigest})
	}
	return refs, nil
}

func acpConnectedProfiles(receipt appserver.CommandResult, preparation controlagents.ACPPreparation, count int) ([]controlagents.ConnectedProfile, error) {
	if count > 0 {
		if receipt.ACPConnect == nil || len(receipt.ACPConnect.Profiles) != count {
			return nil, errors.New("app/gatewayapp/controladapter: committed ACP connect returned an incomplete profile receipt")
		}
		return receipt.ACPConnect.Profiles, nil
	}
	if receipt.Resource == nil || receipt.Resource.Kind != appserver.CommandResourceModelProfile || strings.TrimSpace(receipt.Resource.Ref) == "" {
		return nil, errors.New("app/gatewayapp/controladapter: committed ACP connect returned no ModelProfile resource")
	}
	return []controlagents.ConnectedProfile{{ID: receipt.Resource.Ref, DisplayName: acpPreparedModelDisplayName(preparation)}}, nil
}

func (a *SessionClientAdapter) prepareACPConnectionLocked(ctx context.Context, request controlagents.ConnectRequest, rosterRef string) (controlagents.ACPPreparation, error) {
	request = controlagents.NormalizeConnectRequest(request)
	if request.CWD == "" {
		request.CWD = a.WorkspaceDir()
	}
	key := acpPreparationCacheKey(request)
	if rosterRef != "" {
		key += "\x00" + rosterRef
	}
	preparation, cached := a.acpPreparations[key]
	if pending, ok := a.acpPending[key]; ok {
		var err error
		preparation, err = a.observePendingACPPreparation(ctx, key, pending, nil)
		cached = preparation.Ref != ""
		if err != nil {
			return preparation, err
		}
	}
	if !cached || !preparation.ExpiresAt.After(time.Now()) ||
		(preparation.State != controlagents.PreparationStateNeedsAuth && preparation.State != controlagents.PreparationStateReady) {
		parentRef := rosterRef
		if parentRef == "" && request.ModelID != "" {
			base := request
			base.ModelID = ""
			if parent, ok := a.acpPreparations[acpPreparationCacheKey(base)]; ok &&
				parent.State == controlagents.PreparationStateReady && parent.ExpiresAt.After(time.Now()) {
				parentRef = parent.Ref
			}
		}
		before, err := a.addressedStatus(ctx, "", false)
		if err != nil {
			return controlagents.ACPPreparation{}, fmt.Errorf("app/gatewayapp/controladapter: read Host revision before ACP prepare: %w", err)
		}
		revision := before.Configuration.Revision
		receipt, commandErr := a.agentClient.PrepareACP(ctx, appserver.PrepareACPRequest{
			WriteBase: appserver.WriteBase{
				OperationID:      "agent-acp-prepare-" + uuid.NewString(),
				ExpectedRevision: &revision,
			},
			Request: controlagents.ACPPrepareRequest{
				AdapterID: request.AdapterID, Launcher: request.Launcher,
				CommandLine: request.CommandLine, ModelID: request.ModelID,
				CWD: request.CWD, ParentRef: parentRef,
				Install: request.Install,
			},
		})
		preparation, err = a.observeCommittedACPPreparation(
			ctx, key, "prepare ACP Agent", pendingACPPreparationPrepare, parentRef, receipt, commandErr,
		)
		if err != nil {
			return preparation, err
		}
	} else {
		preparation = controlagents.NormalizeACPPreparation(preparation)
	}
	if preparation.State == controlagents.PreparationStateNeedsAuth {
		methodID, err := selectACPPreparationAuthentication(ctx, preparation)
		if err != nil {
			return controlagents.ACPPreparation{}, err
		}
		current, err := a.addressedStatus(ctx, "", false)
		if err != nil {
			return controlagents.ACPPreparation{}, fmt.Errorf("app/gatewayapp/controladapter: read Host revision before ACP authentication: %w", err)
		}
		revision := current.Configuration.Revision
		authReceipt, authErr := a.agentClient.PrepareACPAuthentication(ctx, appserver.PrepareACPAuthenticationRequest{
			WriteBase: appserver.WriteBase{
				OperationID:      "agent-acp-prepare-auth-" + uuid.NewString(),
				ExpectedRevision: &revision,
			},
			PreparationRef: preparation.Ref, PreparationDigest: preparation.ContentDigest,
			MethodID: methodID,
		})
		parentRef := preparation.Ref
		preparation, err = a.observeCommittedACPPreparation(
			ctx, key, "authenticate ACP Agent", pendingACPPreparationAuth, parentRef, authReceipt, authErr,
		)
		if err != nil {
			return preparation, err
		}
	}
	if preparation.State != controlagents.PreparationStateReady {
		return controlagents.ACPPreparation{}, fmt.Errorf("app/gatewayapp/controladapter: ACP preparation state is %q, want ready", preparation.State)
	}
	a.cacheACPPreparation(key, preparation)
	return preparation, nil
}

func selectACPPreparationAuthentication(ctx context.Context, preparation controlagents.ACPPreparation) (string, error) {
	methods := make([]controlagents.AuthenticationMethod, 0, len(preparation.AuthenticationMethods))
	for _, method := range preparation.AuthenticationMethods {
		methods = append(methods, controlagents.AuthenticationMethod{
			ID: method.ID, Name: method.Name, Description: method.Description, Type: method.Type,
		})
	}
	if len(methods) == 0 {
		return "", errors.New("app/gatewayapp/controladapter: ACP Agent requires authentication but advertised no methods")
	}
	if len(methods) == 1 {
		return methods[0].ID, nil
	}
	selected, err := controlagents.RequestAuthenticationSelection(ctx, controlagents.AuthenticationSelectionRequest{
		AgentID: preparation.Connection.ID, Methods: methods,
	})
	if err != nil {
		return "", fmt.Errorf("app/gatewayapp/controladapter: select ACP authentication method: %w", err)
	}
	for _, method := range methods {
		if method.ID == strings.TrimSpace(selected) {
			return method.ID, nil
		}
	}
	return "", errors.New("app/gatewayapp/controladapter: selected ACP authentication method was not advertised")
}

func acpPreparationCacheKey(request controlagents.ConnectRequest) string {
	request = controlagents.NormalizeConnectRequest(request)
	install, _ := json.Marshal(request.Install)
	return strings.Join([]string{
		request.AdapterID, string(request.Launcher), request.CommandLine, request.CWD, request.ModelID, string(install),
	}, "\x00")
}

func acpPreparedModelDisplayName(preparation controlagents.ACPPreparation) string {
	modelID := strings.TrimSpace(preparation.Request.ModelID)
	for _, model := range preparation.Discovery.Models {
		if model.ID == modelID {
			return strings.TrimSpace(preparation.Connection.Name) + " — " + firstNonEmpty(model.Name, model.ID)
		}
	}
	return strings.TrimSpace(preparation.Connection.Name) + " — " + firstNonEmpty(modelID, "Agent default")
}
