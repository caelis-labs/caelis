package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/discovery"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

// DisconnectCandidates returns only user-configured external ACP Agents. It
// excludes model-backed, built-in, system, and plugin-provided Agents because
// those lifecycles are owned by their respective Control capabilities.
func (s *runtimeComposition) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	snapshot, err := s.DisconnectCandidatesSnapshot(ctx)
	return snapshot.Candidates, err
}

// DisconnectCandidatesSnapshot returns one canonical Host-revision-bound
// roster view for presentation clients preparing a disconnect command.
func (s *runtimeComposition) DisconnectCandidatesSnapshot(ctx context.Context) (appserver.DisconnectCandidatesSnapshot, error) {
	if s == nil || s.store == nil {
		return appserver.DisconnectCandidatesSnapshot{}, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return appserver.DisconnectCandidatesSnapshot{}, err
	}
	doc, err := s.store.LoadContext(ctx)
	if err != nil {
		return appserver.DisconnectCandidatesSnapshot{}, err
	}
	return appserver.DisconnectCandidatesSnapshot{
		Revision:   doc.ConfigurationRevision,
		Candidates: controlagents.ListDisconnectCandidates(doc.ExternalAgents),
	}, nil
}

type externalAgentMutationResult struct {
	Revision      uint64
	EffectStarted bool
	Warning       error
}

// disconnectACPAtRevision removes one connection-scoped external ACP Agent
// and all dependent product configuration through one Host CAS. Adapter
// installation is outside the configuration document and remains retained.
func (s *Stack) disconnectACPAtRevision(ctx context.Context, agentID string, expected uint64) (externalAgentMutationResult, controlagents.DisconnectResult, error) {
	mutation := externalAgentMutationResult{}
	if s == nil || s.composition.store == nil {
		return mutation, controlagents.DisconnectResult{}, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return mutation, controlagents.DisconnectResult{}, err
	}

	doc, err := s.composition.store.LoadContext(ctx)
	if err != nil {
		return mutation, controlagents.DisconnectResult{}, err
	}
	mutation.Revision = doc.ConfigurationRevision
	if doc.ConfigurationRevision != expected {
		return mutation, controlagents.DisconnectResult{}, &configstore.ConfigurationRevisionConflict{
			Expected: expected,
			Actual:   doc.ConfigurationRevision,
		}
	}
	next, result, err := controlagents.DisconnectExternalAgent(doc.ExternalAgents, agentID)
	if err != nil {
		return mutation, controlagents.DisconnectResult{}, fmt.Errorf("gatewayapp: %w", err)
	}
	for _, profile := range modelprofile.NormalizeConfiguration(doc.ModelProfiles).Profiles {
		if profile.Kind() != modelprofile.BackendACP || profile.Backend.ACP.AgentID != result.Agent.ID {
			continue
		}
		doc.AgentBindings, err = agentbinding.PrepareProfileRemoval(doc.AgentBindings, profile.ID)
		if err != nil {
			return mutation, controlagents.DisconnectResult{}, err
		}
		doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, profile.ID)
	}
	doc.ExternalAgents = next
	saved, persistErr := s.composition.store.CompareAndSave(ctx, expected, doc)
	if persistErr != nil && !configstore.WriteCommitted(persistErr) {
		mutation.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
		return mutation, controlagents.DisconnectResult{}, persistErr
	}
	mutation.EffectStarted = true
	mutation.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
	if saved.ConfigurationRevision == 0 {
		reconcileErr := s.reconcileCommittedExternalAgents(ctx)
		return mutation, result, errors.Join(
			persistErr,
			errors.New("gatewayapp: committed external Agent configuration revision is unknown"),
			wrapOptionalError("gatewayapp: reconcile unobserved external Agent configuration", reconcileErr),
		)
	}
	mutation.Warning = wrapOptionalError("gatewayapp: external Agent configuration durability warning", persistErr)
	return mutation, result, nil
}

func (s *Stack) reconcileCommittedExternalAgents(ctx context.Context) error {
	if s == nil || s.composition.store == nil {
		return errors.New("gatewayapp: external Agent configuration is unavailable")
	}
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(contextOrBackground(ctx)), 5*time.Second)
	defer cancel()
	_, err := s.composition.store.LoadContext(reconcileCtx)
	return err
}

func (s *Stack) acpDiscoveryService() discovery.Service {
	return discovery.Service{ClientInfo: &client.Implementation{
		Name:    firstNonEmpty(s.composition.appName, "caelis"),
		Version: version.String(),
	}}
}
