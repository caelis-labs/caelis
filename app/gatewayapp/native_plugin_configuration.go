package gatewayapp

import (
	"context"
	"reflect"
	"strconv"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func (s *controlCommandBackend) updateNativePluginConfiguration(ctx context.Context, scope sessionprofile.Scope, req appserver.UpdateSessionConfigurationRequest) (appserver.CommandResult, error) {
	registry := s.runtimeRegistry()
	if registry == nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(sessionprofile.ErrUnsupported)
	}
	// The same activation gate that publishes native Runtimes keeps a later
	// prompt from sampling the old selector between retirement and commit.
	ctx, unlock, err := registry.lockActivation(ctx)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	defer unlock()
	active, err := s.composition.checkControlCommandCAS(ctx, req.WriteBase)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	if active.Controller.Kind == session.ControllerKindACP {
		return appserver.CommandResult{}, classifyControlPreDispatchError(sessionprofile.ErrUnsupported)
	}
	store := s.composition.authorities.sessionProfiles
	current, err := store.NativeConfiguration(ctx, scope, active)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	if current.Revision != req.ExpectedConfigurationRevision {
		return appserver.CommandResult{}, classifyControlPreDispatchError(sessionprofile.ErrConfigurationStale)
	}
	configuration, err := store.UpdateNativeConfiguration(ctx, scope, active,
		sessionprofile.UpdateConfigurationRequest{OperationID: req.OperationID, ExpectedConfigurationRevision: req.ExpectedConfigurationRevision, Patch: req.Patch},
		func(_ context.Context, selected []sessionprofile.Plugin) error {
			_, err := s.composition.resolveApplicationPlugins(sessionprofile.Profile{Plugins: selected}, applicationPluginPaths(s.composition.authorities.storeDir, active.SessionID, active.CWD))
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current.Native.Plugins, req.Patch.Plugins) {
				return registry.retireIdleNativeConfiguration(ctx, active.SessionRef)
			}
			return nil
		})
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	return appserver.CommandResult{OperationID: req.OperationID, SessionID: req.SessionID, Outcome: appserver.OutcomeCommitted, Configuration: &configuration}, nil
}

// Caller holds activationMu. A native selector changes only at quiescence, like
// ordinary native model configuration. The existing releasing tombstone fences
// loaded command admission; the activation gate fences new Runtime admission.
// Observations can remain connected. No live Turn, Task, approval or fence is
// cancelled to make a configuration update succeed.
func (r *sessionRuntimeRegistry) retireIdleNativeConfiguration(ctx context.Context, ref session.SessionRef) error {
	if err := r.waitSessionRelease(ctx, ref.SessionID); err != nil {
		return err
	}
	conflict := func() error {
		return errorcode.New(errorcode.Conflict, "native plugin configuration requires an idle Session")
	}
	if running, err := r.hasRunningTasks(ctx, ref.SessionID); err != nil {
		return err
	} else if running {
		return conflict()
	}
	if reader, ok := r.sessionService.(session.SessionFenceReader); ok {
		fence, err := reader.SessionFence(ctx, ref)
		if err != nil {
			return err
		}
		if fence.FenceID != "" {
			return conflict()
		}
	} else {
		return errorcode.New(errorcode.Unsupported, "native Session fence observation unavailable")
	}
	r.mu.Lock()
	current := r.sessions[ref.SessionID]
	if current == nil {
		r.mu.Unlock()
		return nil
	}
	if current.releasing {
		r.mu.Unlock()
		// Retirement can start after the initial read. This is a known
		// lifecycle transition, not competing execution or a new command.
		if err := r.waitSessionRelease(ctx, ref.SessionID); err != nil {
			return err
		}
		return r.retireIdleNativeConfiguration(ctx, ref)
	}
	if current.inUse != 0 || current.workRefs != 0 {
		r.mu.Unlock()
		return conflict()
	}
	current.releasing = true
	current.releaseDone = make(chan struct{})
	done := current.releaseDone
	r.mu.Unlock()
	return r.completeRuntimeRelease(ctx, current, done, false)
}

// nativePluginContributions replaces only package contributions when an
// ordinary Session has an explicit override. Nil retains normal Host sampling;
// [] has no fallback. Models, Team, native tools and sandbox are untouched.
func (s *runtimeComposition) nativePluginContributions(configs []PluginConfig) (plugin.Contributions, *sessionprofile.Configuration, error) {
	if s.activation == nil || s.activation.sessionRef.SessionID == "" || s.authorities.sessionProfiles == nil {
		contributions, err := resolveGatewayPluginContributions(configs, s.authorities.storeDir, s.workspace.CWD)
		return contributions, nil, err
	}
	active, err := s.sessions.Session(context.Background(), s.activation.sessionRef)
	if err != nil {
		return plugin.Contributions{}, nil, err
	}
	configuration, err := s.authorities.sessionProfiles.NativeConfiguration(context.Background(), sessionprofile.PrincipalScope(active.UserID), active)
	if err != nil {
		return plugin.Contributions{}, nil, err
	}
	if configuration.Native.Plugins == nil {
		contributions, err := resolveGatewayPluginContributions(configs, s.authorities.storeDir, s.workspace.CWD)
		return contributions, &configuration, err
	}
	contributions, err := applicationRuntimePluginContributions(sessionprofile.Profile{Plugins: *configuration.Native.Plugins}, applicationPluginPaths(s.authorities.storeDir, active.SessionID, s.workspace.CWD), configs...)
	return contributions, &configuration, err
}

func nativePluginStatus(configuration *sessionprofile.Configuration, contributions plugin.Contributions, manager *mcp.Manager) func(uint64) sessionprofile.MCPStatus {
	if configuration == nil {
		return nil
	}
	// Read metadata only during assembly. Status reads never touch packages.
	metas, _ := applicationPluginSkills(contributions.SkillBundles)
	return func(revision uint64) sessionprofile.MCPStatus {
		status := sessionprofile.MCPStatus{ConfigurationRevision: strconv.FormatUint(revision, 10)}
		if revision != configuration.Revision {
			return status
		}
		for _, source := range contributions.Sources {
			status.Plugins = append(status.Plugins, sessionprofile.PluginStatus{ID: source.ID, Root: source.Root, Version: source.Version, Status: pluginSourceStatus(source)})
			for _, info := range manager.GetServerInfos(source.ID) {
				server := applicationMCPServerStatus(info)
				server.PluginID = source.ID
				status.Servers = append(status.Servers, server)
			}
		}
		for _, meta := range metas {
			status.Skills = append(status.Skills, sessionprofile.SkillStatus{Path: meta.Path, Name: meta.Name, Kind: "skill", Status: "ready"})
		}
		return status
	}
}
