package gatewayapp

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/experimental"
)

// ExperimentalFeatures supplies the optional Host feature capability. It borrows
// focused authorities, never a Stack or an executing Runtime lease.
func (s *Stack) ExperimentalFeatures() *appserver.ExperimentalService {
	commands, ok := s.configurationCommands.(appserver.ExperimentalCommandService)
	if !ok {
		return nil
	}
	return &appserver.ExperimentalService{Commands: commands, Reader: experimentalReader{
		store: s.composition.authorities.store, registry: s.sessionRuntimes,
		authorizer: appserver.SessionAuthorizer{Sessions: s.composition.sessions, SessionProfiles: s.composition.authorities.sessionProfiles},
	}}
}

type experimentalReader struct {
	store      *appConfigStore
	registry   *sessionRuntimeRegistry
	authorizer appserver.SessionAuthorizer
}

func (r experimentalReader) ExperimentalFeatures(ctx context.Context, p appserver.Principal, id string) (experimental.Snapshot, error) {
	if strings.TrimSpace(p.ID) == "" {
		return experimental.Snapshot{}, appserver.ErrUnauthorized
	}
	id = strings.TrimSpace(id)
	if id != "" {
		if err := r.authorizer.Authorize(ctx, p, appserver.ActionSessionInspect, id); err != nil {
			return experimental.Snapshot{}, err
		}
	}
	doc, err := r.store.LoadContext(ctx)
	if err != nil {
		return experimental.Snapshot{}, err
	}
	out := experimental.Snapshot{Revision: strconv.FormatUint(doc.ConfigurationRevision, 10), Scope: "host", AppliesAt: "next_runtime_activation", Features: experimental.Definitions(), Desired: experimental.Complete(doc.Experimental), SessionID: id}
	if id != "" && r.registry != nil {
		r.registry.mu.RLock()
		if active := r.registry.sessions[id]; active != nil && !active.releasing && active.instance != nil {
			active.instance.mu.RLock()
			out.Effective = experimental.Complete(active.instance.activeRuntime.Experimental)
			active.instance.mu.RUnlock()
		}
		r.registry.mu.RUnlock()
	}
	return out, nil
}

func (s *controlCommandBackend) executeExperimentalCommand(ctx context.Context, req appserver.ExperimentalRequest) (appserver.CommandResult, error) {
	if err := experimental.Validate(req.Values); err != nil {
		return appserver.CommandResult{}, configurationRejectedError(errorcode.Wrap(errorcode.InvalidArgument, "invalid experimental features", err))
	}
	store := s.composition.authorities.store
	doc, err := store.LoadContext(ctx)
	if err != nil {
		return appserver.CommandResult{}, classifyConfigurationPreEffectError(err)
	}
	expected := expectedConfigurationRevision(req.ExpectedRevision)
	if doc.ConfigurationRevision != expected {
		return configurationCommandResult(doc.ConfigurationRevision), classifyConfigurationPreEffectError(&configstore.ConfigurationRevisionConflict{Expected: expected, Actual: doc.ConfigurationRevision})
	}
	next := experimental.Complete(doc.Experimental)
	maps.Copy(next, req.Values)
	next = experimental.Complete(next)
	_, legacyRefresh := doc.Experimental[experimental.MCPRefresh]
	_, legacyRetirement := doc.Experimental[experimental.ToolRetirement]
	if maps.Equal(next, experimental.Complete(doc.Experimental)) && !legacyRefresh && !legacyRetirement {
		return configurationCommandResult(expected), nil
	}
	doc.Experimental = next
	saved, err := store.CompareAndSave(ctx, expected, doc)
	result := configurationCommandResult(configurationErrorRevision(err, saved.ConfigurationRevision))
	if err != nil {
		if configstore.WriteCommitted(err) {
			return result, appserver.NewOutcomeError(appserver.OutcomeUnknown, errors.Join(errors.New("experimental configuration durability is unconfirmed; read the original operation and configuration"), err))
		}
		return result, classifyConfigurationPreEffectError(err)
	}
	return result, nil
}
