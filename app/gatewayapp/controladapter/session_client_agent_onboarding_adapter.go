package controladapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

func (a *SessionClientAdapter) DiscoverACPConnection(ctx context.Context, request controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	if a == nil || a.agentClient == nil || a.statusClient == nil {
		return controlagents.DiscoverySnapshot{}, errors.New("app/gatewayapp/controladapter: ACP preparation client is unavailable")
	}
	a.acpPreparationMu.Lock()
	defer a.acpPreparationMu.Unlock()
	preparation, err := a.prepareACPConnectionLocked(ctx, request)
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
	preparation, err := a.prepareACPConnectionLocked(ctx, request)
	if err != nil {
		return controlagents.ConnectResult{}, err
	}
	before, err := a.addressedStatus(ctx, "", false)
	if err != nil {
		return controlagents.ConnectResult{}, fmt.Errorf("app/gatewayapp/controladapter: read Host revision before ACP connect: %w", err)
	}
	revision := before.Configuration.Revision
	receipt, commandErr := a.agentClient.ConnectACP(ctx, controlclient.ConnectACPRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:      "agent-acp-connect-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		PreparationRef: preparation.Ref, PreparationDigest: preparation.ContentDigest,
		ConfigValues: request.ConfigValues,
	})
	if receipt.Outcome != controlclient.OutcomeCommitted {
		if commandErr == nil {
			commandErr = fmt.Errorf("ACP connect outcome is %q: %s", receipt.Outcome, strings.TrimSpace(receipt.Detail))
		}
		return controlagents.ConnectResult{}, &controlclient.CommandReceiptError{Receipt: receipt, Err: commandErr}
	}
	if receipt.Resource == nil || receipt.Resource.Kind != controlclient.CommandResourceModelProfile || strings.TrimSpace(receipt.Resource.Ref) == "" {
		return controlagents.ConnectResult{}, &controlclient.CommandReceiptError{
			Receipt: receipt,
			Err:     errors.New("app/gatewayapp/controladapter: committed ACP connect returned no ModelProfile resource"),
		}
	}
	displayName := acpPreparedModelDisplayName(preparation)
	connected := controlagents.ConnectResult{
		Connection: preparation.Connection,
		Discovery:  preparation.Discovery,
		Profiles: []controlagents.ConnectedProfile{{
			ID: receipt.Resource.Ref, DisplayName: displayName,
		}},
	}
	if commandErr != nil || strings.TrimSpace(receipt.Detail) != "" {
		warning := commandErr
		if detail := strings.TrimSpace(receipt.Detail); detail != "" {
			warning = errors.Join(warning, fmt.Errorf(
				"app/gatewayapp/controladapter: ACP connect committed as operation %q with a warning; do not retry blindly: %s",
				receipt.OperationID, detail,
			))
		}
		return connected, &controlclient.CommandReceiptError{Receipt: receipt, Err: warning}
	}
	return connected, nil
}

func (a *SessionClientAdapter) prepareACPConnectionLocked(ctx context.Context, request controlagents.ConnectRequest) (controlagents.ACPPreparation, error) {
	request = controlagents.NormalizeConnectRequest(request)
	if request.CWD == "" {
		request.CWD = a.WorkspaceDir()
	}
	key := acpPreparationCacheKey(request)
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
		parentRef := ""
		if request.ModelID != "" {
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
		receipt, commandErr := a.agentClient.PrepareACP(ctx, controlclient.PrepareACPRequest{
			WriteBase: controlclient.WriteBase{
				OperationID:      "agent-acp-prepare-" + uuid.NewString(),
				ExpectedRevision: &revision,
			},
			Request: controlagents.ACPPrepareRequest{
				AdapterID: request.AdapterID, Launcher: request.Launcher,
				CommandLine: request.CommandLine, ModelID: request.ModelID,
				CWD: request.CWD, ParentRef: parentRef,
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
		authReceipt, authErr := a.agentClient.PrepareACPAuthentication(ctx, controlclient.PrepareACPAuthenticationRequest{
			WriteBase: controlclient.WriteBase{
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
	return strings.Join([]string{
		request.AdapterID, string(request.Launcher), request.CommandLine, request.CWD, request.ModelID,
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
