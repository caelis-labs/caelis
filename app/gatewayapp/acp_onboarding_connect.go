package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	modelprofilebuilder "github.com/caelis-labs/caelis/control/modelprofile/builder"
)

func (s *controlCommandBackend) connectPreparedACPAtRevision(
	ctx context.Context,
	principal appserver.Principal,
	req appserver.ConnectACPRequest,
) (externalAgentMutationResult, []controlagents.ConnectedProfile, error) {
	mutation := externalAgentMutationResult{Revision: expectedConfigurationRevision(req.ExpectedRevision)}
	preparation, err := s.ownedACPPreparation(ctx, principal.ID, req.PreparationRef, req.PreparationDigest)
	if err != nil {
		return mutation, nil, err
	}
	if preparation.State != controlagents.PreparationStateReady {
		return mutation, nil, errors.New("gatewayapp: ACP preparation is not ready")
	}
	selections := []controlagents.ACPPreparation{preparation}
	if len(req.ModelPreparations) > 0 {
		selections, err = s.loadACPConnectSelections(ctx, principal.ID, preparation, req.ModelPreparations)
		if err != nil {
			return mutation, nil, err
		}
	}

	connection := preparation.Connection
	authenticatedAt := preparation.Discovery.DiscoveredAt
	for _, selection := range selections {
		// A probe that needed no chosen method does not supersede a completed login.
		if selection.Connection.Authentication.MethodID != "" &&
			(connection.Authentication.MethodID == "" || selection.Discovery.DiscoveredAt.After(authenticatedAt)) {
			connection.Authentication = selection.Connection.Authentication
			authenticatedAt = selection.Discovery.DiscoveredAt
		}
	}

	doc, err := s.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		return mutation, nil, err
	}
	mutation.Revision = doc.ConfigurationRevision
	expected := expectedConfigurationRevision(req.ExpectedRevision)
	if doc.ConfigurationRevision != expected {
		return mutation, nil, &configstore.ConfigurationRevisionConflict{Expected: expected, Actual: doc.ConfigurationRevision}
	}
	profiles := make([]modelprofile.ModelProfile, 0, len(selections))
	connected := make([]controlagents.ConnectedProfile, 0, len(selections))
	for _, selection := range selections {
		model, defaults, err := controlagents.ResolveDiscoverySelection(selection.Discovery, selection.Request.ModelID, req.ConfigValues)
		if err != nil {
			return mutation, nil, fmt.Errorf("gatewayapp: %w", err)
		}
		next, agent, err := controlagents.UpsertExternalConnection(doc.ExternalAgents, connection, selection.Discovery, externalAgentNameAllowed)
		if err != nil {
			return mutation, nil, fmt.Errorf("gatewayapp: update external Agent configuration: %w", err)
		}
		profile, err := modelprofilebuilder.FromACP(agent, connection, model, defaults, selection.Discovery)
		if err != nil {
			return mutation, nil, fmt.Errorf("gatewayapp: build ACP model profile: %w", err)
		}
		doc.ExternalAgents = next
		profiles = append(profiles, profile)
		connected = append(connected, controlagents.ConnectedProfile{ID: profile.ID, DisplayName: profile.DisplayName})
	}
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, profiles...)
	if err != nil {
		return mutation, nil, fmt.Errorf("gatewayapp: update model profile catalog: %w", err)
	}
	saved, persistErr := s.composition.authorities.store.CompareAndSave(ctx, expected, doc)
	if persistErr != nil && !configstore.WriteCommitted(persistErr) {
		mutation.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
		return mutation, nil, persistErr
	}
	mutation.EffectStarted = true
	mutation.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
	if saved.ConfigurationRevision == 0 {
		reconcileErr := s.reconcileCommittedExternalAgents(ctx)
		return mutation, connected, errors.Join(
			persistErr,
			errors.New("gatewayapp: committed external Agent configuration revision is unknown"),
			wrapOptionalError("gatewayapp: reconcile unobserved external Agent configuration", reconcileErr),
		)
	}
	mutation.Warning = wrapOptionalError("gatewayapp: external Agent configuration durability warning", persistErr)
	return mutation, connected, nil
}

func (s *controlCommandBackend) loadACPConnectSelections(
	ctx context.Context,
	principalID string,
	roster controlagents.ACPPreparation,
	refs []appserver.ACPModelPreparationRef,
) ([]controlagents.ACPPreparation, error) {
	if roster.Request.ModelID != "" || roster.Discovery.SelectedModelID != "" {
		return nil, errors.New("gatewayapp: batch ACP connect requires an unselected roster preparation")
	}
	selections := make([]controlagents.ACPPreparation, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		selection, err := s.ownedACPPreparation(ctx, principalID, ref.Ref, ref.Digest)
		if err != nil {
			return nil, err
		}
		modelID := selection.Request.ModelID
		if selection.State != controlagents.PreparationStateReady || modelID == "" || controlagents.IsDefaultRemoteModelID(modelID) || selection.Discovery.SelectedModelID != modelID {
			return nil, errors.New("gatewayapp: batch ACP connect requires ready, model-scoped preparations for declared models")
		}
		if seen[modelID] {
			return nil, fmt.Errorf("gatewayapp: ACP model %q was selected more than once", modelID)
		}
		if _, _, err := controlagents.ResolveDiscoverySelection(roster.Discovery, modelID, nil); err != nil {
			return nil, fmt.Errorf("gatewayapp: %w", err)
		}
		if selection.ParentRef != roster.Ref {
			// An explicit authentication produces a ready child of its challenge.
			challenge, err := s.ownedACPPreparation(ctx, principalID, selection.ParentRef, "")
			if err != nil {
				return nil, err
			}
			if challenge.State != controlagents.PreparationStateNeedsAuth || challenge.ParentRef != roster.Ref {
				return nil, errors.New("gatewayapp: ACP model preparation belongs to another roster")
			}
		}
		if !sameACPConnectionOwnership(selection.Connection, roster.Connection) || selection.Discovery.CWD != roster.Discovery.CWD {
			return nil, errors.New("gatewayapp: ACP model preparation belongs to another connection or workspace")
		}
		seen[modelID] = true
		selections = append(selections, selection)
	}
	return selections, nil
}
