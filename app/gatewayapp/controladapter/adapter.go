package controladapter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type assembler struct {
	mu                 sync.Mutex
	stack              *RuntimeStack
	session            session.Session
	hasSession         bool
	bindingKey         string
	defaultModelText   string
	modelText          string
	defaultSessionMode string
	sessionMode        string
	defaultSandboxType string
	sandboxType        string
	acpDiscoveries     map[string]acpDiscoveryCacheEntry
	acpEndpointAuth    map[string]acpEndpointAuthCacheEntry
}

// newAssemblerForSession constructs an assembler bound to an already resolved
// session. Session lifecycle and Turn ingress remain owned by typed AppServer
// clients; these private assemblers expose only the server-side projections
// needed by focused status, configuration, Agent, completion, and plugin
// services.
func newAssemblerForSession(ctx context.Context, stack *RuntimeStack, activeSession session.Session, bindingKey string, modelText string) (*assembler, error) {
	if stack == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: stack is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	activeSession = session.CloneSession(activeSession)
	if strings.TrimSpace(activeSession.SessionID) == "" {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: session id is required")
	}
	if activeSession.AppName == "" {
		activeSession.AppName = strings.TrimSpace(stack.Session.AppName)
	}
	if activeSession.UserID == "" {
		activeSession.UserID = strings.TrimSpace(stack.Session.UserID)
	}
	driver := newAssemblerForStack(stack, bindingKey, modelText)
	driver.bindSession(ctx, activeSession)
	return driver, nil
}

func newAssemblerForStack(stack *RuntimeStack, bindingKey string, modelText string) *assembler {
	key := firstNonEmpty(strings.TrimSpace(bindingKey), "cli-tui")
	return &assembler{
		stack:              stack,
		bindingKey:         key,
		defaultModelText:   strings.TrimSpace(modelText),
		modelText:          strings.TrimSpace(modelText),
		defaultSessionMode: "auto-review",
		sessionMode:        "auto-review",
		defaultSandboxType: "auto",
		sandboxType:        "auto",
	}
}

func (d *assembler) bindSession(ctx context.Context, activeSession session.Session) {
	activeSession = session.CloneSession(activeSession)
	d.session = activeSession
	d.hasSession = true
	d.refreshSessionDisplay(ctx, activeSession)
}

func (d *assembler) gatewayTurns() (GatewayTurnService, error) {
	return resolveGatewayDep(d, gatewayTurnServiceFn, "gateway turn service", "gateway turn service is unavailable")
}

func (d *assembler) gatewaySessions() (GatewaySessionService, error) {
	return resolveGatewayDep(d, gatewaySessionServiceFn, "gateway session service", "gateway session service is unavailable")
}

func (d *assembler) gatewayControlPlane() (GatewayControlPlaneService, error) {
	return resolveGatewayDep(d, gatewayControlPlaneServiceFn, "gateway control-plane service", "gateway control-plane service is unavailable")
}

func resolveGatewayDep[T any](driver *assembler, provider func(GatewayRuntimeDeps) func() T, depName, unavailable string) (T, error) {
	var zero T
	if driver == nil || driver.stack == nil {
		return zero, fmt.Errorf("app/gatewayapp/controladapter: stack is required")
	}
	fn := provider(driver.stack.Gateway)
	if fn == nil {
		return zero, missingRuntimeDependency(depName)
	}
	if gw := fn(); any(gw) != nil {
		return gw, nil
	}
	return zero, fmt.Errorf("app/gatewayapp/controladapter: %s", unavailable)
}

func gatewayTurnServiceFn(deps GatewayRuntimeDeps) func() GatewayTurnService {
	return deps.TurnServiceFn
}

func gatewaySessionServiceFn(deps GatewayRuntimeDeps) func() GatewaySessionService {
	return deps.SessionServiceFn
}

func gatewayControlPlaneServiceFn(deps GatewayRuntimeDeps) func() GatewayControlPlaneService {
	return deps.ControlPlaneServiceFn
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func (d *assembler) WorkspaceDir() string {
	if d == nil || d.stack == nil {
		return ""
	}
	if activeSession, ok := d.currentSession(); ok {
		if cwd := strings.TrimSpace(activeSession.CWD); cwd != "" {
			return cwd
		}
	}
	return strings.TrimSpace(d.stack.Session.Workspace.CWD)
}

func (d *assembler) requireSession() (session.Session, error) {
	if activeSession, ok := d.currentSession(); ok {
		return activeSession, nil
	}
	return session.Session{}, fmt.Errorf("app/gatewayapp/controladapter: no bound session")
}

func (d *assembler) currentSession() (session.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasSession {
		return session.Session{}, false
	}
	return d.session, true
}

func (d *assembler) activeACPControllerStatus(ctx context.Context) (controller.ControllerStatus, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.stack == nil {
		return controller.ControllerStatus{}, false, nil
	}
	activeSession, ok := d.currentSession()
	if !ok || activeSession.Controller.Kind != session.ControllerKindACP {
		return controller.ControllerStatus{}, false, nil
	}
	status := controller.ControllerStatus{}
	found := false
	if d.stack.Agent.ControllerStatusFn != nil {
		var err error
		status, found, err = d.stack.Agent.ControllerStatusFn(ctx, activeSession.SessionRef)
		if err != nil {
			return controller.ControllerStatus{}, false, err
		}
	}
	if !found {
		status = controller.ControllerStatus{
			SessionRef:      activeSession.SessionRef,
			Agent:           firstNonEmpty(strings.TrimSpace(activeSession.Controller.AgentName), strings.TrimSpace(activeSession.Controller.Label), strings.TrimSpace(activeSession.Controller.ControllerID)),
			RemoteSessionID: strings.TrimSpace(activeSession.Controller.RemoteSessionID),
		}
	}
	return status, true, nil
}

func activeTurnKindForSession(active []kernel.ActiveTurnState, ref session.SessionRef) (string, bool) {
	state, ok := activeTurnStateForSession(active, ref)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(string(state.Kind)), true
}

func activeTurnStateForSession(active []kernel.ActiveTurnState, ref session.SessionRef) (kernel.ActiveTurnState, bool) {
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return kernel.ActiveTurnState{}, false
	}
	for _, item := range active {
		if strings.TrimSpace(item.SessionRef.SessionID) == sessionID {
			return item, true
		}
	}
	return kernel.ActiveTurnState{}, false
}

func noActiveTurnSubmissionError() error {
	return kernel.NoActiveRunError("")
}

func (d *assembler) listResumeCandidates(ctx context.Context, limit int) ([]controlprompt.ResumeCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	ctx, cancel := completionContext(ctx, resumeCompletionTimeout)
	defer cancel()
	gw, err := d.gatewaySessions()
	if err != nil {
		return nil, err
	}
	result, err := gw.ListSessions(ctx, kernel.ListSessionsRequest{
		AppName:      d.stack.Session.AppName,
		UserID:       d.stack.Session.UserID,
		WorkspaceKey: d.stack.Session.Workspace.Key,
		Limit:        limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]controlprompt.ResumeCandidate, 0, len(result.Sessions))
	for _, session := range result.Sessions {
		candidate := enrichResumeCandidate(ctx, d.stack.Session.Store, session)
		if strings.TrimSpace(candidate.Prompt) == "" && strings.TrimSpace(candidate.Title) == "" {
			continue
		}
		out = append(out, candidate)
	}
	return out, nil
}

func (d *assembler) ListAgents(ctx context.Context, limit int) ([]controlprompt.AgentCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	return d.agentCatalog(limit), nil
}

func (d *assembler) AgentStatus(ctx context.Context) (controlprompt.AgentStatusSnapshot, error) {
	status := controlprompt.AgentStatusSnapshot{
		AvailableAgents: d.agentCatalog(0),
	}
	activeSession, ok := d.currentSession()
	if !ok {
		return status, nil
	}
	gw, err := d.gatewayControlPlane()
	if err != nil {
		return controlprompt.AgentStatusSnapshot{}, err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		return controlprompt.AgentStatusSnapshot{}, err
	}
	status.SessionID = activeSession.SessionID
	status.ControllerKind = string(state.Controller.Kind)
	status.ControllerLabel = strings.TrimSpace(firstNonEmpty(state.Controller.AgentName, state.Controller.Label, state.Controller.ControllerID, string(state.Controller.Kind)))
	status.ControllerEpoch = strings.TrimSpace(state.Controller.EpochID)
	status.HasActiveTurn = state.HasActiveTurn
	if turns, err := d.gatewayTurns(); err == nil && turns != nil {
		if kind, ok := activeTurnKindForSession(turns.ActiveTurns(), activeSession.SessionRef); ok {
			status.HasActiveTurn = true
			status.ActiveTurnKind = kind
		}
	}
	if state.Controller.Kind == session.ControllerKindACP {
		if controllerStatus, ok, err := d.activeACPControllerStatus(ctx); err != nil {
			return controlprompt.AgentStatusSnapshot{}, err
		} else if ok {
			status.ControllerModel = strings.TrimSpace(controllerStatus.Model)
			status.ControllerReasoningEffort = strings.TrimSpace(controllerStatus.ReasoningEffort)
			status.ControllerCommands = controllerCommandNames(controllerStatus.Commands)
			status.ControllerModels = controllerChoicesToSlashCandidates(controllerStatus.ModelOptions, "remote ACP model", "", 0)
			status.ControllerEfforts = controllerChoicesToSlashCandidates(controllerStatus.EffortOptions, "remote ACP reasoning effort", "", 0)
		}
	}
	status.Participants = make([]controlprompt.AgentParticipantSnapshot, 0, len(state.Participants))
	status.DelegatedParticipants = make([]controlprompt.AgentParticipantSnapshot, 0)
	for _, participant := range state.Participants {
		snapshot := agentParticipantSnapshot(participant)
		if participant.Kind == session.ParticipantKindSubagent && participant.Role == session.ParticipantRoleDelegated {
			status.DelegatedParticipants = append(status.DelegatedParticipants, snapshot)
			continue
		}
		status.Participants = append(status.Participants, snapshot)
	}
	return status, nil
}

func agentParticipantSnapshot(participant kernel.ParticipantState) controlprompt.AgentParticipantSnapshot {
	return controlprompt.AgentParticipantSnapshot{
		ID:        strings.TrimSpace(participant.ID),
		Label:     strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID)),
		AgentName: strings.TrimSpace(firstNonEmpty(participant.AgentName, participant.Label, participant.ID)),
		Kind:      string(participant.Kind),
		Role:      string(participant.Role),
		Source:    strings.TrimSpace(participant.Source),
		SessionID: strings.TrimSpace(participant.SessionID),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func formatReasoningModelDisplay(alias string, effort string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ""
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" || effort == "none" {
		return alias
	}
	return alias + " [" + effort + "]"
}

func dedupeNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func humanAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	delta := time.Since(t).Round(time.Minute)
	if delta < time.Minute {
		return "just now"
	}
	return delta.String() + " ago"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (d *assembler) defaultDisplays() (string, string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.defaultModelText, d.defaultSessionMode, d.defaultSandboxType
}

func (d *assembler) refreshSessionDisplay(ctx context.Context, activeSession session.Session) {
	if d == nil || d.stack == nil {
		return
	}
	modelText, sessionMode, sandboxType := d.defaultDisplays()
	if d.stack.Model.DefaultAliasFn != nil {
		if alias := strings.TrimSpace(d.stack.Model.DefaultAliasFn()); alias != "" {
			modelText = alias
		}
	}
	if d.stack.Status.RuntimeStateFn != nil {
		if state, err := d.stack.Status.RuntimeStateFn(ctx, activeSession.SessionRef); err == nil {
			if strings.TrimSpace(state.ModelAlias) != "" {
				modelText = strings.TrimSpace(state.ModelAlias)
			}
			if strings.TrimSpace(state.SessionMode) != "" {
				sessionMode = strings.TrimSpace(state.SessionMode)
			}
		}
	}
	d.mu.Lock()
	d.modelText = modelText
	d.sessionMode = sessionMode
	d.sandboxType = sandboxType
	d.mu.Unlock()
}
