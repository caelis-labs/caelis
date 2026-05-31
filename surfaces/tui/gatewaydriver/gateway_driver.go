package gatewaydriver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type GatewayDriver struct {
	mu                  sync.Mutex
	stack               *DriverStack
	session             coresession.Session
	hasSession          bool
	bindingKey          string
	defaultModelText    string
	modelText           string
	defaultSessionMode  string
	sessionMode         string
	defaultSandboxType  string
	sandboxType         string
	activeCommandID     uint64
	activeCommandCancel context.CancelFunc
}

func NewGatewayDriver(ctx context.Context, stack *DriverStack, preferredSessionID string, bindingKey string, modelText string) (*GatewayDriver, error) {
	if stack == nil {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: stack is required")
	}
	key := firstNonEmpty(strings.TrimSpace(bindingKey), "cli-tui")
	if ctx == nil {
		ctx = context.Background()
	}
	driver := &GatewayDriver{
		stack:              stack,
		bindingKey:         key,
		defaultModelText:   strings.TrimSpace(modelText),
		modelText:          strings.TrimSpace(modelText),
		defaultSessionMode: "auto-review",
		sessionMode:        "auto-review",
		defaultSandboxType: "auto",
		sandboxType:        "auto",
	}
	if preferredSessionID = strings.TrimSpace(preferredSessionID); preferredSessionID != "" {
		activeCoreSession, err := driver.stack.StartSession(ctx, preferredSessionID, driver.bindingKey)
		if err != nil {
			return nil, err
		}
		driver.session = activeCoreSession
		driver.hasSession = true
		driver.refreshSessionDisplay(ctx, activeCoreSession)
	}
	return driver, nil
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

func (d *GatewayDriver) WorkspaceDir() string {
	if d == nil || d.stack == nil {
		return ""
	}
	return strings.TrimSpace(d.stack.Workspace.CWD)
}

func (d *GatewayDriver) ensureSession(ctx context.Context) (coresession.Session, error) {
	if activeSession, ok := d.currentSession(); ok {
		return activeSession, nil
	}
	if d == nil || d.stack == nil {
		return coresession.Session{}, fmt.Errorf("surfaces/tui/gatewaydriver: stack is unavailable")
	}
	activeCoreSession, err := d.stack.StartSession(ctx, "", d.bindingKey)
	if err != nil {
		return coresession.Session{}, err
	}
	d.mu.Lock()
	d.session = activeCoreSession
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, activeCoreSession)
	return activeCoreSession, nil
}

func (d *GatewayDriver) currentSession() (coresession.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasSession {
		return coresession.Session{}, false
	}
	return d.session, true
}

func (d *GatewayDriver) activeACPControllerStatus(ctx context.Context) (appviewmodel.ControllerStatus, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.stack == nil {
		return appviewmodel.ControllerStatus{}, false, nil
	}
	activeSession, ok := d.currentSession()
	if !ok || activeSession.Controller.Kind != coresession.ControllerACP {
		return appviewmodel.ControllerStatus{}, false, nil
	}
	status, found, err := d.stack.ACPControllerStatus(ctx, activeSession.Ref)
	if err != nil {
		return appviewmodel.ControllerStatus{}, false, err
	}
	if !found {
		status = appviewmodel.ControllerStatus{
			SessionRef:      activeSession.Ref,
			Agent:           firstNonEmpty(strings.TrimSpace(activeSession.Controller.AgentName), strings.TrimSpace(activeSession.Controller.Label), strings.TrimSpace(activeSession.Controller.ID)),
			RemoteSessionID: strings.TrimSpace(activeSession.Controller.RemoteSessionID),
		}
	}
	return status, true, nil
}

func (d *GatewayDriver) LightweightStatus(ctx context.Context) (StatusSnapshot, error) {
	return d.statusFromAppView(ctx, false)
}

func (d *GatewayDriver) Status(ctx context.Context) (StatusSnapshot, error) {
	return d.statusFromAppView(ctx, true)
}

func (d *GatewayDriver) HomeView(ctx context.Context, version string) (appviewmodel.HomeView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.stack == nil {
		return appviewmodel.HomeView{}, fmt.Errorf("surfaces/tui/gatewaydriver: stack is unavailable")
	}
	ref := coresession.Ref{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.Ref
	}
	view, ok, err := d.stack.HomeView(ctx, ref, version)
	if err != nil || ok {
		return view, err
	}
	return appviewmodel.HomeView{}, fmt.Errorf("surfaces/tui/gatewaydriver: home view dependency is unavailable")
}

func (d *GatewayDriver) Submit(ctx context.Context, submission Submission) (Turn, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	input := strings.TrimSpace(submission.Text)
	contentParts, err := contentPartsFromSubmission(input, submission.Attachments, d.WorkspaceDir())
	if err != nil {
		return nil, err
	}
	if isBuiltInControllerSession(activeSession) && activeKernelTurnForSession(d.stack.ActiveTurns(), activeSession.Ref) {
		coreSub := coreruntime.Submission{
			Kind:         coreruntime.SubmissionConversation,
			Text:         input,
			ContentParts: contentParts,
			Meta: map[string]any{
				"submission_mode": string(submission.Mode),
				"display_text":    strings.TrimSpace(submission.DisplayText),
			},
		}
		if d.stack != nil && d.stack.SubmitActiveTurnFn != nil {
			err = d.stack.SubmitActiveTurnFn(ctx, SubmitActiveTurnRequest{
				SessionRef: activeSession.Ref,
				Submission: coreSub,
			})
		} else {
			err = fmt.Errorf("surfaces/tui/gatewaydriver: active-turn submission dependency is unavailable")
		}
		if err == nil {
			return nil, nil
		}
		if !isNoActiveRunError(err) {
			return nil, err
		}
	}
	if d.stack != nil && d.stack.BeginTurnFn != nil {
		result, err := d.stack.BeginTurnFn(ctx, BeginTurnRequest{
			SessionRef:   activeSession.Ref,
			Input:        input,
			ContentParts: contentParts,
			Surface:      d.bindingKey,
			Meta: map[string]any{
				"submission_mode": string(submission.Mode),
				"display_text":    strings.TrimSpace(submission.DisplayText),
			},
		})
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.session = result.Session
		d.hasSession = true
		d.mu.Unlock()
		return result.Turn, nil
	}
	return nil, fmt.Errorf("surfaces/tui/gatewaydriver: begin turn dependency is unavailable")
}

func activeKernelTurnForSession(active []ActiveTurnState, ref coresession.Ref) bool {
	kind, ok := activeTurnKindForSession(active, ref)
	if !ok {
		return false
	}
	return kind == "" || kind == ActiveTurnKindKernel
}

func activeTurnKindForSession(active []ActiveTurnState, ref coresession.Ref) (ActiveTurnKind, bool) {
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return "", false
	}
	for _, item := range active {
		if strings.TrimSpace(item.SessionRef.SessionID) == sessionID {
			return ActiveTurnKind(strings.TrimSpace(string(item.Kind))), true
		}
	}
	return "", false
}

func isBuiltInControllerSession(activeSession coresession.Session) bool {
	switch activeSession.Controller.Kind {
	case "", coresession.ControllerBuiltin:
		return true
	default:
		return false
	}
}

func isNoActiveRunError(err error) bool {
	return errors.Is(err, errNoActiveRun)
}

func (d *GatewayDriver) Interrupt(ctx context.Context) error {
	cancelCommand := d.activeCommandInterrupt()
	if cancelCommand != nil {
		cancelCommand()
	}
	activeSession, ok := d.currentSession()
	if !ok {
		if cancelCommand != nil {
			return nil
		}
		return fmt.Errorf("surfaces/tui/gatewaydriver: no active session")
	}
	if err := d.stack.Interrupt(ctx, InterruptRequest{
		SessionRef: activeSession.Ref,
		Surface:    d.bindingKey,
		Reason:     "tui interrupt",
	}); err != nil {
		if cancelCommand != nil {
			return nil
		}
		return err
	}
	return nil
}

func (d *GatewayDriver) beginInterruptibleCommand(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	commandCtx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	d.activeCommandID++
	id := d.activeCommandID
	d.activeCommandCancel = cancel
	d.mu.Unlock()
	return commandCtx, func() {
		d.mu.Lock()
		if d.activeCommandID == id {
			d.activeCommandCancel = nil
		}
		d.mu.Unlock()
		cancel()
	}
}

func (d *GatewayDriver) activeCommandInterrupt() context.CancelFunc {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.activeCommandCancel
}

func (d *GatewayDriver) NewSession(ctx context.Context) (coresession.Session, error) {
	activeCoreSession, err := d.stack.StartSession(ctx, "", d.bindingKey)
	if err != nil {
		return coresession.Session{}, err
	}
	d.mu.Lock()
	d.session = activeCoreSession
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, activeCoreSession)
	return activeCoreSession, nil
}

func (d *GatewayDriver) ResumeSession(ctx context.Context, sessionID string) (coresession.Session, error) {
	result, err := d.stack.ResumeSession(ctx, ResumeSessionRequest{
		SessionID: strings.TrimSpace(sessionID),
		Surface:   d.bindingKey,
	})
	if err != nil {
		return coresession.Session{}, err
	}
	d.mu.Lock()
	d.session = result
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, result)
	return result, nil
}

func (d *GatewayDriver) ListSessions(ctx context.Context, limit int) ([]ResumeCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	ctx, cancel := completionContext(ctx, resumeCompletionTimeout)
	defer cancel()
	return d.stack.ListSessionCandidates(ctx, ListSessionCandidatesRequest{
		Workspace: coresession.Workspace{
			Key: strings.TrimSpace(d.stack.Workspace.Key),
			CWD: strings.TrimSpace(d.stack.Workspace.CWD),
		},
		Limit: limit,
	})
}

func (d *GatewayDriver) ReplaySessionEvents(ctx context.Context) ([]appviewmodel.SessionEventEnvelope, error) {
	activeSession, ok := d.currentSession()
	if !ok {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: no active session")
	}
	if d == nil || d.stack == nil || d.stack.ReplaySessionEventsFn == nil {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: session replay dependency is unavailable")
	}
	return d.stack.ReplaySessionEventsFn(ctx, activeSession.Ref)
}

func (d *GatewayDriver) Compact(ctx context.Context) error {
	activeSession, ok := d.currentSession()
	if !ok {
		return fmt.Errorf("surfaces/tui/gatewaydriver: no active session")
	}
	return d.stack.CompactSession(ctx, activeSession.Ref)
}

func (d *GatewayDriver) ListAgents(ctx context.Context, limit int) ([]AgentCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	return d.agentCatalog(limit), nil
}

func (d *GatewayDriver) AgentStatus(ctx context.Context) (AgentStatusSnapshot, error) {
	status := AgentStatusSnapshot{
		AvailableAgents: d.agentCatalog(0),
	}
	activeSession, ok := d.currentSession()
	if !ok {
		return status, nil
	}
	ref := activeSession.Ref
	state, err := d.stack.ControlPlaneState(ctx, ref)
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	status.SessionID = activeSession.SessionID
	status.ControllerKind = agentControllerKindDisplay(state.Controller.Kind)
	status.ControllerLabel = strings.TrimSpace(firstNonEmpty(state.Controller.AgentName, state.Controller.Label, state.Controller.ID, string(state.Controller.Kind)))
	status.ControllerEpoch = strings.TrimSpace(state.Controller.EpochID)
	status.HasActiveTurn = state.HasActiveTurn
	if kind, ok := activeTurnKindForSession(d.stack.ActiveTurns(), ref); ok {
		status.HasActiveTurn = true
		status.ActiveTurnKind = string(kind)
	}
	if state.Controller.Kind == coresession.ControllerACP {
		if controllerStatus, ok, err := d.activeACPControllerStatus(ctx); err != nil {
			return AgentStatusSnapshot{}, err
		} else if ok {
			status.ControllerModel = strings.TrimSpace(controllerStatus.Model)
			status.ControllerReasoningEffort = strings.TrimSpace(controllerStatus.ReasoningEffort)
			status.ControllerCommands = controllerCommandNames(controllerStatus.Commands)
			status.ControllerModels = controllerChoicesToSlashCandidates(controllerStatus.ModelOptions, "remote ACP model", "", 0)
			status.ControllerEfforts = controllerChoicesToSlashCandidates(controllerStatus.EffortOptions, "remote ACP reasoning effort", "", 0)
		}
	}
	status.Participants = make([]AgentParticipantSnapshot, 0, len(state.Participants))
	status.DelegatedParticipants = make([]AgentParticipantSnapshot, 0)
	for _, participant := range state.Participants {
		snapshot := agentParticipantSnapshot(participant)
		if participant.Kind == coresession.ParticipantSubagent && participant.Role == coresession.ParticipantDelegated {
			status.DelegatedParticipants = append(status.DelegatedParticipants, snapshot)
			continue
		}
		status.Participants = append(status.Participants, snapshot)
	}
	return status, nil
}

func agentParticipantSnapshot(participant coresession.ParticipantBinding) AgentParticipantSnapshot {
	return AgentParticipantSnapshot{
		ID:        strings.TrimSpace(participant.ID),
		Label:     strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID)),
		AgentName: strings.TrimSpace(firstNonEmpty(participant.AgentName, participant.Label, participant.ID)),
		Kind:      string(participant.Kind),
		Role:      string(participant.Role),
		SessionID: strings.TrimSpace(participant.SessionID),
	}
}

func agentControllerKindDisplay(kind coresession.ControllerKind) string {
	switch kind {
	case "", coresession.ControllerBuiltin:
		return "kernel"
	default:
		return string(kind)
	}
}

func (d *GatewayDriver) ContinueSubagent(ctx context.Context, handle string, prompt string, attachments []Attachment) (Turn, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	prompt = strings.TrimSpace(prompt)
	contentParts, err := contentPartsFromSubmission(prompt, attachments, d.WorkspaceDir())
	if err != nil {
		return nil, err
	}
	participantID, err := d.resolveParticipantID(ctx, activeSession.Ref, handle)
	if err != nil {
		return nil, err
	}
	if d.stack != nil && d.stack.PromptParticipantFn != nil {
		result, err := d.stack.PromptParticipantFn(ctx, PromptParticipantRequest{
			SessionRef:    activeSession.Ref,
			ParticipantID: participantID,
			Input:         prompt,
			ContentParts:  contentParts,
			Source:        "user_side_agent",
		})
		if err != nil {
			return nil, err
		}
		if result.Session.Ref.SessionID != "" {
			d.mu.Lock()
			d.session = result.Session
			d.hasSession = true
			d.mu.Unlock()
		}
		return result.Turn, nil
	}
	return nil, fmt.Errorf("surfaces/tui/gatewaydriver: participant prompt dependency is unavailable")
}

func validateConnectConfig(tpl providerTemplate, cfg ConnectConfig) error {
	if strings.TrimSpace(cfg.Model) == "" {
		return fmt.Errorf("model is required; use /connect and choose or type a model name")
	}
	if baseURL := strings.TrimSpace(cfg.BaseURL); baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("base URL is invalid; use a full URL such as %s", tpl.DefaultBaseURL)
		}
	}
	if tpl.NoAuthRequired {
		return nil
	}
	if strings.TrimSpace(cfg.APIKey) != "" || strings.TrimSpace(cfg.TokenEnv) != "" {
		return nil
	}
	envHint := defaultTokenEnvNameForConnect(tpl.Provider, cfg.BaseURL)
	if envHint == "" {
		envHint = "YOUR_API_KEY"
	}
	return fmt.Errorf("API key is missing; paste a key or enter env:%s in /connect", envHint)
}

func parseTokenEnvSpec(value string) (string, bool) {
	return appservices.ParseConnectTokenEnvSpec(value)
}

func defaultTokenEnvName(provider string) string {
	return appservices.DefaultConnectTokenEnvName(provider, "")
}

func defaultTokenEnvNameForConnect(provider string, baseURL string) string {
	return appservices.DefaultConnectTokenEnvName(provider, baseURL)
}

func defaultConnectAuthType(provider string) model.AuthType {
	return appservices.DefaultConnectAuthType(provider)
}

func isXiaomiTokenPlanProvider(provider string) bool {
	return appservices.IsXiaomiTokenPlanProvider(provider)
}

func isXiaomiTokenPlanBaseURL(baseURL string) bool {
	return appservices.IsXiaomiTokenPlanBaseURL(baseURL)
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

func (d *GatewayDriver) refreshSessionDisplay(ctx context.Context, activeSession coresession.Session) {
	if d == nil || d.stack == nil {
		return
	}
	d.mu.Lock()
	d.modelText = d.defaultModelText
	d.sessionMode = d.defaultSessionMode
	d.sandboxType = d.defaultSandboxType
	d.mu.Unlock()
}

func authTypeFromString(s string) model.AuthType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "api_key", "apikey":
		return model.AuthAPIKey
	case "bearer_token", "bearer":
		return model.AuthBearerToken
	case "oauth_token", "oauth":
		return model.AuthOAuthToken
	case "none":
		return model.AuthNone
	default:
		return model.AuthAPIKey
	}
}
