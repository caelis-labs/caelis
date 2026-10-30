package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	bridge "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// The local AppServer supplies its complete focused client assembly once. The
// shared pointer is borrowed by Runtime snapshots; no leaf service retains Stack.
type applicationChildClientFactory struct {
	mu              sync.RWMutex
	bind            func(appserver.Principal) (appserver.AppServerClients, error)
	sessionProfiles *appserver.SessionProfileService
}

type applicationChildren struct {
	composition       *runtimeComposition
	binding           sessionprofile.Binding
	active            session.Session
	mainModelSnapshot func(context.Context) (AppConfig, *modelLookup, error)
}

// Team roles use the canonical Runtime revision, including provider parameters.
// Session main-model pins belong only to the explicit self participant.
func (c *applicationChildren) snapshot(ctx context.Context) (*placementSnapshot, *modelLookup, error) {
	snapshot, err := c.composition.placementSnapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	return snapshot, snapshot.lookup, nil
}

func (c *applicationChildren) selfSnapshot(ctx context.Context) (*placementSnapshot, *modelLookup, error) {
	if c.mainModelSnapshot != nil {
		doc, lookup, err := c.mainModelSnapshot(ctx)
		if err != nil {
			return nil, nil, err
		}
		return newPlacementSnapshot(doc), lookup, nil
	}
	return c.snapshot(ctx)
}

func (c *applicationChildren) targetSnapshot(ctx context.Context, selector string) (*placementSnapshot, *modelLookup, error) {
	if selector == string(agentbinding.HandleSelf) {
		return c.selfSnapshot(ctx)
	}
	return c.snapshot(ctx)
}

func (c *applicationChildren) principal() appserver.Principal {
	return appserver.Principal{ID: c.binding.PrincipalID}
}

func (c *applicationChildren) source(ctx context.Context) (sessionprofile.Source, error) {
	value, ok := ctx.Value(applicationSourceContextKey{}).(sessionprofile.Source)
	if !ok && c.binding.Child != nil {
		value, ok = c.binding.Child.Source, true
	}
	if !ok || sessionprofile.ValidateSource(value) != nil {
		return sessionprofile.Source{}, errors.New("application child: trusted parent source is required")
	}
	if err := c.checkSource(ctx, value); err != nil {
		return sessionprofile.Source{}, err
	}
	return value, nil
}

// Explicit participant commands use the native authenticated user-input
// contract. Model-initiated work retains its already-admitted source instead.
func (c *applicationChildren) admitParticipant(ctx context.Context, operation string) (context.Context, error) {
	if sessionvisibility.IsSpawnedSubagentSession(c.active) {
		return ctx, errors.New("application child: nested delegation is unavailable")
	}
	if _, ok := ctx.Value(applicationSourceContextKey{}).(sessionprofile.Source); !ok {
		source := sessionprofile.Source{Kind: "user", OperationID: operation}
		if c.binding.Child != nil {
			source = c.binding.Child.Source
		}
		ctx = context.WithValue(ctx, applicationSourceContextKey{}, source)
	}
	_, err := c.source(ctx)
	return ctx, err
}

func (c *applicationChildren) checkSource(ctx context.Context, source sessionprofile.Source) error {
	store := c.composition.authorities.sessionProfiles
	if err := store.CheckActive(ctx, c.binding.Scope); err != nil {
		return err
	}
	root := c.binding.SessionID
	if c.binding.Child != nil {
		root = c.binding.Child.RootSessionID
	}
	bound, err := store.GetBinding(ctx, c.binding.Scope, root)
	if err != nil {
		return err
	}
	if bound.Archived {
		return appserver.ErrSessionClosed
	}
	closed, err := appserver.IsSessionClosed(ctx, c.composition.sessions, session.SessionRef{SessionID: root})
	if err != nil {
		return err
	}
	if closed {
		return appserver.ErrSessionClosed
	}
	if source.Kind == "authorized_background" {
		admitted, err := store.AdmitBackgroundSource(ctx, c.binding.Scope, root, source.GrantID, source.OperationID)
		if err != nil {
			return err
		}
		if admitted != source {
			return appserver.ErrUnauthorized
		}
	}
	return nil
}

// self uses the same explicit configured provider and normal frozen-placement
// owner as every registered native model. It never samples the Host default.
func (c *applicationChildren) selfPlacement(ctx context.Context) (placement.Placement, error) {
	configuration, err := c.composition.authorities.sessionProfiles.Configuration(ctx, c.binding.Scope, c.binding.SessionID)
	if err != nil {
		return placement.Placement{}, err
	}
	snapshot, lookup, err := c.selfSnapshot(ctx)
	if err != nil {
		return placement.Placement{}, err
	}
	configured, err := applicationModelConfig(lookup, configuration.Profile)
	if err != nil {
		return placement.Placement{}, err
	}
	for _, profile := range snapshot.placement.Profiles.Profiles {
		if profile.Backend.Provider != nil && profile.Backend.Provider.ModelConfigID == configured.ID {
			return controlplacement.ResolveHandle(snapshot.placement, controlplacement.HandleRequest{Handle: agentbinding.HandleSelf, Purpose: controlplacement.PurposeSpawn,
				Session: controlplacement.SessionContext{ProfileID: profile.ID, Effort: configured.ReasoningEffort, Speed: profile.SelectedSpeed(placement.Placement{ServiceTier: configuration.Profile.ServiceTier})}})
		}
	}
	return placement.Placement{}, errors.New("application child: configured provider profile is unavailable")
}

func (c *applicationChildren) handles(ctx context.Context) ([]string, error) {
	if sessionvisibility.IsSpawnedSubagentSession(c.active) {
		return []string{}, nil
	}
	snapshot, _, err := c.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	result := []string{"self"}
	for _, binding := range agentbinding.NormalizeConfiguration(snapshot.placement.Bindings).Bindings {
		if !agentbinding.CatalogFor(snapshot.placement.Bindings).IsDelegation(binding.Handle) {
			continue
		}
		resolved, err := controlplacement.ResolveHandle(snapshot.placement, controlplacement.HandleRequest{Handle: binding.Handle, Purpose: controlplacement.PurposeSpawn})
		if err == nil && resolved.Kind == placement.KindModel {
			result = append(result, string(binding.Handle))
		}
	}
	return result, nil
}

func (c *applicationChildren) placement(ctx context.Context, handle agentbinding.Handle) (placement.Placement, error) {
	if sessionvisibility.IsSpawnedSubagentSession(c.active) {
		return placement.Placement{}, errors.New("application child: nested delegation is unavailable")
	}
	if handle == agentbinding.HandleSelf {
		return c.selfPlacement(ctx)
	}
	snapshot, _, err := c.snapshot(ctx)
	if err != nil {
		return placement.Placement{}, err
	}
	resolved, err := controlplacement.ResolveHandle(snapshot.placement, controlplacement.HandleRequest{Handle: handle, Purpose: controlplacement.PurposeSpawn})
	if err != nil {
		return placement.Placement{}, err
	}
	if resolved.Kind != placement.KindModel {
		return placement.Placement{}, errors.New("application child: external ACP endpoints do not have a profile-bound authority contract")
	}
	return resolved, nil
}

func (c *applicationChildren) agents(ctx context.Context) ([]assembly.AgentConfig, error) {
	ctx, err := c.composition.captureTeamSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	handles, err := c.handles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]assembly.AgentConfig, 0, len(handles))
	for _, handle := range handles {
		frozen, err := c.placement(ctx, agentbinding.Handle(handle))
		if err != nil {
			return nil, err
		}
		_, lookup, err := c.targetSnapshot(ctx, handle)
		if err != nil {
			return nil, err
		}
		configured, err := lookup.ResolveConfig(frozen.Model)
		if err != nil {
			return nil, err
		}
		configured.ReasoningEffort = frozen.ReasoningEffort
		out = append(out, assembly.AgentConfig{Name: handle, Description: "Application profile participant", HostedAdapterID: "application-profile", ACPDraftSchema: acpdraft.SchemaTag, BuiltinRuntime: true, PinnedModel: &configured})
	}
	return out, nil
}

func (c *applicationChildren) resolve(ctx context.Context, parent subagent.SpawnContext, request delegation.TargetRequest) (assembly.AgentConfig, error) {
	if parent.SessionRef.SessionID != c.binding.SessionID || sessionvisibility.IsSpawnedSubagentSession(c.active) {
		return assembly.AgentConfig{}, appserver.ErrUnauthorized
	}
	if _, err := c.source(ctx); err != nil {
		return assembly.AgentConfig{}, err
	}
	snapshot, lookup, err := c.targetSnapshot(ctx, request.Target.Selector)
	if err != nil {
		return assembly.AgentConfig{}, err
	}
	frozen := request.Target.Placement
	if frozen.Kind != placement.KindModel {
		return assembly.AgentConfig{}, errors.New("application child: only registered native provider placements are supported")
	}
	if err := controlplacement.ValidateFrozen(snapshot.placement, frozen); err != nil {
		return assembly.AgentConfig{}, err
	}
	configured, err := lookup.ResolveConfig(frozen.Model)
	if err != nil {
		return assembly.AgentConfig{}, err
	}
	configured.ReasoningEffort = frozen.ReasoningEffort
	// Use an owned logical name rather than self's ambient CLI launch flags.
	return assembly.AgentConfig{Name: "application-profile", Description: "Application profile child", HostedAdapterID: "application-profile", ACPDraftSchema: acpdraft.SchemaTag, BuiltinRuntime: true, PinnedModel: &configured,
		SessionOptions: caelisModelSessionOptions(configured, frozen.ReasoningEffort, frozen.ServiceTier)}, nil
}

func (c *applicationChildren) tools(ctx context.Context) ([]tool.Tool, error) {
	result := c.composition.collaborationTools(c.active)
	if sessionvisibility.IsSpawnedSubagentSession(c.active) {
		return c.leaseTools(result), nil
	}
	handles, err := c.handles(ctx)
	if err != nil {
		return nil, err
	}
	agents := make([]delegation.Agent, 0, len(handles))
	targets := make(map[string]spawn.Target, len(handles))
	for _, handle := range handles {
		frozen, err := c.placement(ctx, agentbinding.Handle(handle))
		if err != nil {
			return nil, err
		}
		agents = append(agents, delegation.Agent{Name: handle, Description: "Independent child using this Application profile"})
		targets[handle] = spawn.Target{Selector: handle, Placement: frozen}
	}
	return append(c.leaseTools(result), spawn.NewWithTargets(agents, targets).WithAdmission(func(ctx context.Context) error {
		_, err := c.source(ctx)
		return err
	})), nil
}

func (c *applicationChildren) leaseTools(tools []tool.Tool) []tool.Tool {
	for i, item := range tools {
		tools[i] = applicationChildTool{Tool: applicationLeasedTool{Tool: item, store: c.composition.authorities.sessionProfiles, scope: c.binding.Scope}, owner: c}
	}
	return tools
}

// Approval and provider waits do not extend an Application lease or a root
// background grant. Recheck both at the original collaboration tool owner.
type applicationChildTool struct {
	tool.Tool
	owner *applicationChildren
}

func (t applicationChildTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if _, err := t.owner.source(ctx); err != nil {
		return tool.Result{}, err
	}
	ctx, err := t.owner.composition.captureTeamSnapshot(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	return t.Tool.Call(ctx, call)
}

func (c *applicationChildren) prepare(ctx context.Context, id string, cfg assembly.AgentConfig) (controlagents.SessionOptions, error) {
	bound, err := c.composition.authorities.sessionProfiles.GetBinding(ctx, c.binding.Scope, id)
	if err != nil {
		return controlagents.SessionOptions{}, err
	}
	if bound.Child == nil || bound.Child.ParentSessionID != c.binding.SessionID {
		return controlagents.SessionOptions{}, appserver.ErrUnauthorized
	}
	if cfg.PinnedModel == nil {
		return controlagents.SessionOptions{}, errors.New("application child: resolved model preparation is required")
	}
	store := c.composition.authorities.sessionProfiles
	current, err := store.Configuration(ctx, bound.Scope, id)
	if err != nil {
		return controlagents.SessionOptions{}, err
	}
	model, effort, tier := cfg.PinnedModel.ID, cfg.PinnedModel.ReasoningEffort, ""
	if cfg.SessionOptions.ConfigValues["service_tier"] == "fast" {
		tier = "priority"
	}
	if current.Profile.Model != model || current.Profile.ReasoningEffort != effort || current.Profile.ServiceTier != tier {
		factory := c.composition.authorities.applicationChildClients
		if factory == nil {
			return controlagents.SessionOptions{}, errors.New("Session configuration service unavailable")
		}
		factory.mu.RLock()
		service := factory.sessionProfiles
		factory.mu.RUnlock()
		if service == nil {
			return controlagents.SessionOptions{}, errors.New("Session configuration service unavailable")
		}
		result, updateErr := service.UpdateSessionConfiguration(ctx, c.principal(), appserver.UpdateSessionConfigurationRequest{
			WriteBase: appserver.WriteBase{OperationID: "prepare-child-" + id, SessionID: id}, ExpectedConfigurationRevision: current.Revision,
			Patch: sessionprofile.ConfigurationPatch{Model: &model, ReasoningEffort: &effort, ServiceTier: &tier}})
		if updateErr != nil {
			return controlagents.SessionOptions{}, updateErr
		}
		if result.Outcome != appserver.OutcomeCommitted || result.Configuration == nil {
			return controlagents.SessionOptions{}, errors.New("child configuration has no committed receipt")
		}

	}
	return controlagents.SessionOptions{}, err
}

func (c *applicationChildren) ResolveACPProcess(ctx context.Context, req endpoint.Request) (endpoint.Process, error) {
	if req.AdapterID != "application-profile" || req.ACPDraftSchema != acpdraft.SchemaTag || req.CWD != c.active.CWD {
		return endpoint.Process{}, appserver.ErrUnauthorized
	}
	source, err := c.source(ctx)
	if err != nil {
		return endpoint.Process{}, err
	}
	return endpoint.Process{Open: func(ctx context.Context) (endpoint.Stream, error) {
		factory := c.composition.authorities.applicationChildClients
		if factory == nil {
			return endpoint.Stream{}, errors.New("application child: typed AppServer assembly is unavailable")
		}
		factory.mu.RLock()
		bind, sessionProfiles := factory.bind, factory.sessionProfiles
		factory.mu.RUnlock()
		if bind == nil || sessionProfiles == nil {
			return endpoint.Stream{}, errors.New("application child: typed AppServer assembly is unavailable")
		}
		clients, err := bind(c.principal())
		if err != nil {
			return endpoint.Stream{}, err
		}
		child := &applicationChildSessions{SessionClient: clients.Sessions, owner: c, source: source, sessionProfiles: sessionProfiles}
		clients.Sessions = child
		clients.Presentation = applicationChildPresentation{PresentationClient: clients.Presentation, imageInput: req.ModelImageInput}
		serverCtx, cancel := context.WithCancel(ctx)
		agent, err := bridge.NewGatewayDraftAgent(serverCtx, bridge.GatewayAgentConfig{Clients: clients, SystemSessionClient: child, AppName: c.composition.authorities.appName, UserID: c.binding.PrincipalID, WorkspaceKey: c.active.WorkspaceKey, WorkspaceCWD: c.active.CWD,
			SlashResultFormatter: func(result controlprompt.SlashCommandResult) string {
				raw, _ := json.Marshal(result)
				return string(raw)
			}})
		if err != nil {
			cancel()
			return endpoint.Stream{}, err
		}
		inReader, inWriter := io.Pipe()
		outReader, outWriter := io.Pipe()
		done := make(chan struct{})
		var serveErr error
		var once sync.Once
		go func() {
			defer close(done)
			defer inReader.Close()
			defer outWriter.Close()
			serveErr = bridge.ServeDraftProductStreams(serverCtx, &applicationDraftChild{DraftAgent: agent, sessions: child}, inReader, outWriter)
		}()
		return endpoint.Stream{Input: inWriter, Output: outReader, Close: func() error { once.Do(func() { cancel(); _ = inWriter.Close(); _ = outReader.Close() }); return nil }, Wait: func(ctx context.Context) error {
			select {
			case <-done:
				if errors.Is(serveErr, context.Canceled) {
					return nil
				}
				return serveErr
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}}, nil
	}}, nil
}

type applicationChildSessions struct {
	appserver.SessionClient
	owner           *applicationChildren
	source          sessionprofile.Source
	sessionProfiles *appserver.SessionProfileService
}

// ACP negotiation projects the frozen child model's existing capability owner.
// It does not grant access to global Host presentation or substitute the parent.
type applicationChildPresentation struct {
	appserver.PresentationClient
	imageInput *bool
}

func (p applicationChildPresentation) PresentationCapabilities(context.Context) (appserver.PresentationCapabilities, error) {
	return appserver.PresentationCapabilities{Image: p.imageInput != nil && *p.imageInput}, nil
}

func (s *applicationChildSessions) binding(ctx context.Context, id string) (sessionprofile.Binding, error) {
	bound, err := s.owner.composition.authorities.sessionProfiles.GetBinding(ctx, s.owner.binding.Scope, id)
	if err != nil {
		return sessionprofile.Binding{}, err
	}
	if bound.Child == nil || bound.Child.ParentSessionID != s.owner.binding.SessionID {
		return sessionprofile.Binding{}, appserver.ErrUnauthorized
	}
	return bound, nil
}

func (s *applicationChildSessions) CreateSession(ctx context.Context, req appserver.CreateSessionRequest) (appserver.CommandResult, error) {
	c := s.owner
	if err := c.checkSource(ctx, s.source); err != nil {
		return appserver.CommandResult{}, err
	}
	if strings.TrimSpace(req.CWD) != c.active.CWD {
		return appserver.CommandResult{}, appserver.ErrUnauthorized
	}
	parent, _ := req.Metadata[sessionvisibility.MetadataSystemManagedParent].(string)
	task, _ := req.Metadata[sessionvisibility.MetadataSystemManagedTask].(string)
	if parent != "" && parent != c.binding.SessionID {
		return appserver.CommandResult{}, appserver.ErrUnauthorized
	}
	configuration, err := c.composition.authorities.sessionProfiles.Configuration(ctx, c.binding.Scope, c.binding.SessionID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	root := c.binding.SessionID
	if c.binding.Child != nil {
		root = c.binding.Child.RootSessionID
	}
	id := sessionprofile.SessionID(c.binding.Scope, "child:"+c.binding.SessionID+":"+req.OperationID)
	relation := &sessionprofile.ChildRelation{RootSessionID: root, ParentSessionID: c.binding.SessionID, ParentTaskID: task, Source: s.source}
	profile := configuration.Profile
	profile.Workspace.CWD = c.active.CWD
	encoded, _ := json.Marshal(struct {
		Profile   sessionprofile.Profile
		Relation  *sessionprofile.ChildRelation
		Operation string
	}{profile, relation, req.OperationID})
	hash := sha256.Sum256(encoded)
	digest := hex.EncodeToString(hash[:])
	bound := sessionprofile.Binding{Scope: c.binding.Scope, SessionID: id, Profile: profile, CreationDigest: digest, Child: relation}
	active, err := c.composition.sessions.StartSession(ctx, session.StartSessionRequest{PreferredSessionID: id, AppName: c.active.AppName, UserID: c.binding.PrincipalID,
		Workspace: session.WorkspaceRef{Key: id, CWD: c.active.CWD}, Controller: initialKernelControllerBinding("acp_ingress"), ExecutionConfig: c.active.ExecutionConfig,
		Metadata: map[string]any{sessionvisibility.MetadataSystemManagedAgent: sessionvisibility.SystemManagedAgentApplication, sessionvisibility.MetadataSystemManagedParent: c.binding.SessionID, sessionvisibility.MetadataSystemManagedTask: task, "application_creation_digest": digest}})
	if err != nil {
		return sessionCommandResult(active), err
	}
	if err = c.composition.authorities.sessionProfiles.PutBinding(ctx, bound); err != nil {
		return sessionCommandResult(active), err
	}
	return sessionCommandResult(active), nil
}

func (s *applicationChildSessions) InspectSession(ctx context.Context, req appserver.StateRequest) (appserver.SessionState, error) {
	if _, err := s.binding(ctx, req.SessionID); err != nil {
		return appserver.SessionState{}, err
	}
	return s.SessionClient.InspectSession(ctx, req)
}
func (s *applicationChildSessions) Reconnect(ctx context.Context, req appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	if _, err := s.binding(ctx, req.SessionID); err != nil {
		return appserver.ReconnectResult{}, err
	}
	return s.SessionClient.Reconnect(ctx, req)
}
func (s *applicationChildSessions) Prompt(ctx context.Context, req appserver.PromptRequest) (appserver.CommandResult, error) {
	bound, err := s.binding(ctx, req.SessionID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	req.SourceKind, req.GrantID = bound.Child.Source.Kind, bound.Child.Source.GrantID
	return s.SessionClient.Prompt(ctx, req)
}
func (s *applicationChildSessions) RecordInteraction(ctx context.Context, req appserver.RecordInteractionRequest) (appserver.CommandResult, error) {
	if _, err := s.binding(ctx, req.SessionID); err != nil {
		return appserver.CommandResult{}, err
	}
	client, ok := s.SessionClient.(appserver.InteractionClient)
	if !ok {
		return appserver.CommandResult{}, errors.New("application child: interaction owner is unavailable")
	}
	return client.RecordInteraction(ctx, req)
}

// The local channel fills only the factual native relation on reattachment.
// The scoped Session client still checks the exact persisted parent binding.
type applicationDraftChild struct {
	*bridge.DraftAgent
	sessions *applicationChildSessions
}

func (a *applicationDraftChild) ResumeSession(ctx context.Context, req v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
	bound, err := a.sessions.binding(ctx, string(req.SessionId))
	if err != nil {
		return v2.ResumeSessionResponse{}, err
	}
	if len(req.Meta) == 0 {
		raw, _ := json.Marshal(map[string]any{"version": 1, "runtime": map[string]any{"session": map[string]any{"kind": "subagent", "parent_session_id": bound.Child.ParentSessionID, "task_id": bound.Child.ParentTaskID}}})
		req.Meta = map[string]json.RawMessage{"caelis": raw}
	}
	return a.DraftAgent.ResumeSession(ctx, req)
}

// Preserve exact native decision options and identities in inherited methods.
var _ appserver.InteractionClient = (*applicationChildSessions)(nil)
var _ endpoint.Resolver = (*applicationChildren)(nil)
