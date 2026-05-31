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
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

type GatewayDriver struct {
	mu                  sync.Mutex
	stack               *DriverStack
	session             session.Session
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
	streamSubscriptions map[string]struct{}
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
		stack:               stack,
		bindingKey:          key,
		defaultModelText:    strings.TrimSpace(modelText),
		modelText:           strings.TrimSpace(modelText),
		defaultSessionMode:  "auto-review",
		sessionMode:         "auto-review",
		defaultSandboxType:  "auto",
		sandboxType:         "auto",
		streamSubscriptions: map[string]struct{}{},
	}
	if preferredSessionID = strings.TrimSpace(preferredSessionID); preferredSessionID != "" {
		activeSession, err := driver.stack.StartSession(ctx, preferredSessionID, driver.bindingKey)
		if err != nil {
			return nil, err
		}
		driver.session = activeSession
		driver.hasSession = true
		driver.refreshSessionDisplay(ctx, activeSession)
	}
	return driver, nil
}

func (d *GatewayDriver) gateway() (GatewayService, error) {
	if d == nil || d.stack == nil {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: stack is required")
	}
	return d.stack.gateway()
}

func (d *GatewayDriver) SubscribeStream(ctx context.Context, env kernel.EventEnvelope) (<-chan kernel.EventEnvelope, bool) {
	gw, err := d.gateway()
	if err != nil {
		return nil, false
	}
	req, ok := kernel.StreamRequestFromEvent(env)
	if !ok {
		return nil, false
	}
	streams := gw.Streams()
	if streams == nil {
		return nil, false
	}
	key := req.Key()
	if key == "" {
		return nil, false
	}
	d.mu.Lock()
	if d.streamSubscriptions == nil {
		d.streamSubscriptions = map[string]struct{}{}
	}
	if _, exists := d.streamSubscriptions[key]; exists {
		d.mu.Unlock()
		return nil, false
	}
	d.streamSubscriptions[key] = struct{}{}
	d.mu.Unlock()

	out := make(chan kernel.EventEnvelope, 32)
	go func() {
		defer close(out)
		defer func() {
			d.mu.Lock()
			delete(d.streamSubscriptions, key)
			d.mu.Unlock()
		}()
		for frame, err := range streams.Subscribe(ctx, stream.SubscribeRequest{Ref: req.Ref, Cursor: req.Cursor}) {
			if err != nil || frame == nil {
				return
			}
			if frame.Text == "" && frame.Event == nil && !frame.Closed {
				continue
			}
			for _, env := range kernel.StreamFrameEvents(req, stream.CloneFrame(*frame)) {
				select {
				case out <- env:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, true
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

func (d *GatewayDriver) ensureSession(ctx context.Context) (session.Session, error) {
	if activeSession, ok := d.currentSession(); ok {
		return activeSession, nil
	}
	if d == nil || d.stack == nil {
		return session.Session{}, fmt.Errorf("surfaces/tui/gatewaydriver: stack is unavailable")
	}
	activeSession, err := d.stack.StartSession(ctx, "", d.bindingKey)
	if err != nil {
		return session.Session{}, err
	}
	d.mu.Lock()
	d.session = activeSession
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, activeSession)
	return activeSession, nil
}

func (d *GatewayDriver) currentSession() (session.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasSession {
		return session.Session{}, false
	}
	return d.session, true
}

func (d *GatewayDriver) activeACPControllerStatus(ctx context.Context) (controller.ControllerStatus, bool, error) {
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
	status, found, err := d.stack.ACPControllerStatus(ctx, activeSession.SessionRef)
	if err != nil {
		return controller.ControllerStatus{}, false, err
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

func (d *GatewayDriver) LightweightStatus(ctx context.Context) (StatusSnapshot, error) {
	return d.status(ctx, false)
}

func (d *GatewayDriver) Status(ctx context.Context) (StatusSnapshot, error) {
	return d.status(ctx, true)
}

func (d *GatewayDriver) status(ctx context.Context, includeDiagnostics bool) (StatusSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if status, ok, err := d.statusFromAppView(ctx); ok || err != nil {
		return status, err
	}
	modelText, sessionMode, sandboxType := d.defaultDisplays()
	reasoningEffort := ""
	if d.stack != nil {
		if alias := strings.TrimSpace(d.stack.DefaultModelAlias()); alias != "" {
			modelText = alias
		}
	}
	sandboxStatus := SandboxStatus{}
	if includeDiagnostics && d.stack != nil {
		sandboxStatus = d.stack.SandboxStatus()
	}
	activeSession, ok := d.currentSession()
	if ok && d.stack != nil {
		if state, err := d.stack.SessionRuntimeState(context.Background(), activeSession.SessionRef); err == nil {
			if strings.TrimSpace(state.ModelAlias) != "" {
				modelText = strings.TrimSpace(state.ModelAlias)
			}
			if strings.TrimSpace(state.ReasoningEffort) != "" {
				reasoningEffort = strings.TrimSpace(state.ReasoningEffort)
			}
			if strings.TrimSpace(state.SessionMode) != "" {
				sessionMode = strings.TrimSpace(state.SessionMode)
			}
		}
	}
	acpStatus, activeACP, acpStatusErr := d.activeACPControllerStatus(ctx)
	if acpStatusErr != nil {
		return StatusSnapshot{}, acpStatusErr
	}
	acpModeID := ""
	acpModeLabel := ""
	acpModelText := ""
	if activeACP {
		acpModelText = acpControllerModelText(acpStatus, activeSession)
		modelText = acpModelText
		reasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
		acpModeID = strings.TrimSpace(acpStatus.Mode)
		acpModeLabel = acpControllerModeDisplay(acpStatus)
	}
	sandboxType = firstNonEmpty(sandboxStatus.ResolvedBackend, sandboxStatus.RequestedBackend, sandboxType)
	route := sandboxStatus.Route
	securitySummary := sandboxStatus.SecuritySummary
	d.mu.Lock()
	sessionID := ""
	if ok {
		sessionID = activeSession.SessionID
	}
	liveModelText := d.modelText
	liveSessionMode := d.sessionMode
	liveSandboxType := d.sandboxType
	bindingKey := d.bindingKey
	d.mu.Unlock()
	rawModelText := firstNonEmpty(modelText, liveModelText)
	workspaceCWD := strings.TrimSpace(d.stack.Workspace.CWD)

	status := StatusSnapshot{
		SessionID:                       sessionID,
		Workspace:                       workspaceStatusDisplay(ctx, workspaceCWD),
		Model:                           formatReasoningModelDisplay(rawModelText, reasoningEffort),
		ReasoningEffort:                 reasoningEffort,
		ModeLabel:                       firstNonEmpty(sessionMode, liveSessionMode),
		SessionMode:                     firstNonEmpty(sessionMode, liveSessionMode),
		SandboxType:                     firstNonEmpty(sandboxType, liveSandboxType),
		SandboxRequestedBackend:         firstNonEmpty(sandboxStatus.RequestedBackend, "auto"),
		SandboxResolvedBackend:          firstNonEmpty(sandboxStatus.ResolvedBackend, sandboxStatus.RequestedBackend, liveSandboxType),
		Route:                           route,
		FallbackReason:                  sandboxStatus.FallbackReason,
		SandboxInstallHint:              sandboxStatus.InstallHint,
		SandboxSetup:                    sandbox.CloneSetupStatus(sandboxStatus.Setup),
		SandboxSetupRequired:            sandboxStatus.SetupRequired,
		SandboxSetupError:               sandboxStatus.SetupError,
		SandboxSetupMarkerCurrent:       sandboxStatus.SetupMarkerCurrent,
		SandboxSetupMarkerReason:        sandboxStatus.SetupMarkerReason,
		SandboxGlobalSetupCurrent:       sandboxStatus.GlobalSetupCurrent,
		SandboxGlobalSetupRequired:      sandboxStatus.GlobalSetupRequired,
		SandboxGlobalSetupReason:        sandboxStatus.GlobalSetupReason,
		SandboxWorkspaceSetupCurrent:    sandboxStatus.WorkspaceSetupCurrent,
		SandboxWorkspaceSetupRequired:   sandboxStatus.WorkspaceSetupRequired,
		SandboxWorkspaceSetupReason:     sandboxStatus.WorkspaceSetupReason,
		SandboxWorkspaceSetupRoot:       sandboxStatus.WorkspaceSetupRoot,
		SandboxWorkspaceSetupWriteRoots: sandboxStatus.WorkspaceSetupWriteRoots,
		SandboxWorkspaceSetupPolicyHash: sandboxStatus.WorkspaceSetupPolicyHash,
		SandboxWorkspaceSetupUpdatedAt:  sandboxStatus.WorkspaceSetupUpdatedAt,
		SecuritySummary:                 securitySummary,
		HostExecution:                   strings.EqualFold(strings.TrimSpace(route), "host"),
		FullAccessMode:                  false,
		Surface:                         bindingKey,
	}
	if d.stack != nil {
		req := DoctorRequest{}
		if ok {
			req.SessionRef = activeSession.SessionRef
		}
		if includeDiagnostics {
			if report, err := d.stack.Doctor(context.Background(), req); err == nil {
				status.StoreDir = strings.TrimSpace(report.StoreDir)
				status.Provider = strings.TrimSpace(report.ActiveProvider)
				status.ModelName = strings.TrimSpace(report.ActiveModel)
				status.MissingAPIKey = report.MissingAPIKey
				status.HostExecution = report.HostExecution
				status.FullAccessMode = report.FullAccessMode
				status.PermissionGrantCount = report.PermissionGrantCount
				status.PermissionReadRootCount = report.PermissionReadRootCount
				status.PermissionWriteRootCount = report.PermissionWriteRootCount
				status.SandboxRequestedBackend = firstNonEmpty(strings.TrimSpace(report.SandboxRequestedBackend), status.SandboxRequestedBackend)
				status.SandboxResolvedBackend = firstNonEmpty(strings.TrimSpace(report.SandboxResolvedBackend), status.SandboxResolvedBackend)
				status.Route = firstNonEmpty(strings.TrimSpace(report.SandboxRoute), status.Route)
				status.FallbackReason = firstNonEmpty(strings.TrimSpace(report.SandboxFallbackReason), status.FallbackReason)
				status.SandboxInstallHint = firstNonEmpty(strings.TrimSpace(report.SandboxInstallHint), status.SandboxInstallHint)
				if report.SandboxSetup != nil {
					status.SandboxSetup = sandbox.CloneSetupStatus(*report.SandboxSetup)
				}
				status.SandboxSetupRequired = report.SandboxSetupRequired || status.SandboxSetupRequired
				status.SandboxSetupError = firstNonEmpty(strings.TrimSpace(report.SandboxSetupError), status.SandboxSetupError)
				status.SandboxSetupMarkerCurrent = report.SandboxSetupMarkerCurrent || status.SandboxSetupMarkerCurrent
				status.SandboxSetupMarkerReason = firstNonEmpty(strings.TrimSpace(report.SandboxSetupMarkerReason), status.SandboxSetupMarkerReason)
				status.SandboxGlobalSetupCurrent = report.SandboxGlobalSetupCurrent || status.SandboxGlobalSetupCurrent
				status.SandboxGlobalSetupRequired = report.SandboxGlobalSetupRequired || status.SandboxGlobalSetupRequired
				status.SandboxGlobalSetupReason = firstNonEmpty(strings.TrimSpace(report.SandboxGlobalSetupReason), status.SandboxGlobalSetupReason)
				status.SandboxWorkspaceSetupCurrent = report.SandboxWorkspaceSetupCurrent || status.SandboxWorkspaceSetupCurrent
				status.SandboxWorkspaceSetupRequired = report.SandboxWorkspaceSetupRequired || status.SandboxWorkspaceSetupRequired
				status.SandboxWorkspaceSetupReason = firstNonEmpty(strings.TrimSpace(report.SandboxWorkspaceSetupReason), status.SandboxWorkspaceSetupReason)
				status.SandboxWorkspaceSetupRoot = firstNonEmpty(strings.TrimSpace(report.SandboxWorkspaceSetupRoot), status.SandboxWorkspaceSetupRoot)
				if report.SandboxWorkspaceSetupWriteRoots > 0 {
					status.SandboxWorkspaceSetupWriteRoots = report.SandboxWorkspaceSetupWriteRoots
				}
				status.SandboxWorkspaceSetupPolicyHash = firstNonEmpty(strings.TrimSpace(report.SandboxWorkspaceSetupPolicyHash), status.SandboxWorkspaceSetupPolicyHash)
				if !report.SandboxWorkspaceSetupUpdatedAt.IsZero() {
					status.SandboxWorkspaceSetupUpdatedAt = report.SandboxWorkspaceSetupUpdatedAt
				}
				status.SecuritySummary = firstNonEmpty(strings.TrimSpace(report.SandboxSecuritySummary), status.SecuritySummary)
				if alias := strings.TrimSpace(report.ActiveModelAlias); alias != "" {
					rawModelText = alias
					status.Model = formatReasoningModelDisplay(alias, status.ReasoningEffort)
				}
				if mode := strings.TrimSpace(report.SessionMode); mode != "" {
					status.ModeLabel = mode
					status.SessionMode = mode
				}
				if id := strings.TrimSpace(report.SessionID); id != "" {
					status.SessionID = id
				}
			}
		}
		if status.ReasoningEffort == "" {
			if activeACP {
				status.ReasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
				status.Model = formatReasoningModelDisplay(firstNonEmpty(strings.TrimSpace(acpStatus.Model), rawModelText), status.ReasoningEffort)
			} else if cfg, ok := d.stack.ModelConfig(rawModelText); ok {
				status.ReasoningEffort = firstNonEmpty(cfg.ReasoningEffort, cfg.DefaultReasoningEffort)
				status.Model = formatReasoningModelDisplay(rawModelText, status.ReasoningEffort)
			}
		}
		if ok {
			if usage, err := d.sessionTokenUsageBreakdown(context.Background(), activeSession.SessionRef); err == nil {
				status.SessionUsageTotal = usage.Total
				status.SessionUsageMain = usage.Main
				status.SessionUsageSubagents = usage.Subagents
				status.SessionUsageAutoReview = usage.AutoReview
				status.SessionUsageCompaction = usage.Compaction
				status.SessionInputTokens = usage.Total.PromptTokens
				status.SessionCachedInputTokens = usage.Total.CachedInputTokens
				status.SessionOutputTokens = usage.Total.CompletionTokens
				status.SessionReasoningTokens = usage.Total.ReasoningTokens
				status.SessionTotalTokens = usage.Total.TotalTokens
			}
		}
	}
	if activeACP {
		rawModelText = firstNonEmpty(strings.TrimSpace(acpStatus.Model), acpModelText, rawModelText)
		status.Model = formatReasoningModelDisplay(rawModelText, strings.TrimSpace(acpStatus.ReasoningEffort))
		status.ReasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
		if acpModeID != "" {
			status.SessionMode = acpModeID
		}
		if acpModeLabel != "" || acpModeID != "" {
			status.ModeLabel = firstNonEmpty(acpModeLabel, acpModeID)
		}
		status.Provider = "acp"
		status.ModelName = strings.TrimSpace(acpStatus.Model)
		status.MissingAPIKey = false
		status.FullAccessMode = false
		status.PromptTokens = 0
		status.CompletionTokens = 0
		status.TotalTokens = 0
		status.ContextWindowTokens = 0
	}
	if status.TotalTokens > 0 {
		status.PromptTokens = status.TotalTokens
	}
	if status.FullAccessMode {
		status.HostExecution = true
		status.Route = firstNonEmpty(strings.TrimSpace(status.Route), "host")
		if strings.TrimSpace(status.Route) != "host" {
			status.Route = "host"
		}
	}
	if gw, err := d.gateway(); err == nil && gw != nil {
		active := gw.ActiveTurns()
		status.ActiveJobs = len(active)
		status.Running = len(active) > 0
		if kind, ok := activeTurnKindForSession(active, activeSession.SessionRef); ok {
			status.ActiveTurnKind = kind
		}
	}
	return status, nil
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
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	if isBuiltInControllerSession(activeSession) && activeKernelTurnForSession(gw.ActiveTurns(), activeSession.SessionRef) {
		err := gw.SubmitActiveTurn(ctx, kernel.SubmitActiveTurnRequest{
			SessionRef:   activeSession.SessionRef,
			Kind:         kernel.SubmissionKindConversation,
			Text:         input,
			ContentParts: contentParts,
			Metadata: map[string]any{
				"submission_mode": string(submission.Mode),
				"display_text":    strings.TrimSpace(submission.DisplayText),
			},
		})
		if err == nil {
			return nil, nil
		}
		if !isNoActiveRunError(err) {
			return nil, err
		}
	}
	result, err := gw.BeginTurn(ctx, kernel.BeginTurnRequest{
		SessionRef:   activeSession.SessionRef,
		Input:        input,
		ContentParts: contentParts,
		Surface:      d.bindingKey,
		Metadata: map[string]any{
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
	if result.Handle == nil {
		return nil, nil
	}
	return gatewayTurn{handle: result.Handle}, nil
}

func activeKernelTurnForSession(active []kernel.ActiveTurnState, ref session.SessionRef) bool {
	kind, ok := activeTurnKindForSession(active, ref)
	if !ok {
		return false
	}
	return kind == "" || strings.EqualFold(kind, string(kernel.ActiveTurnKindKernel))
}

func activeTurnKindForSession(active []kernel.ActiveTurnState, ref session.SessionRef) (string, bool) {
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return "", false
	}
	for _, item := range active {
		if strings.TrimSpace(item.SessionRef.SessionID) == sessionID {
			return strings.TrimSpace(string(item.Kind)), true
		}
	}
	return "", false
}

func isBuiltInControllerSession(activeSession session.Session) bool {
	switch activeSession.Controller.Kind {
	case "", session.ControllerKindKernel:
		return true
	default:
		return false
	}
}

func isNoActiveRunError(err error) bool {
	var gwErr *kernel.Error
	return errors.As(err, &gwErr) && gwErr.Code == kernel.CodeNoActiveRun
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
	gw, err := d.gateway()
	if err != nil {
		return err
	}
	if err := gw.Interrupt(ctx, kernel.InterruptRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: d.bindingKey,
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

func (d *GatewayDriver) NewSession(ctx context.Context) (session.Session, error) {
	activeSession, err := d.stack.StartSession(ctx, "", d.bindingKey)
	if err != nil {
		return session.Session{}, err
	}
	d.mu.Lock()
	d.session = activeSession
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, activeSession)
	return activeSession, nil
}

func (d *GatewayDriver) ResumeSession(ctx context.Context, sessionID string) (session.Session, error) {
	gw, err := d.gateway()
	if err != nil {
		return session.Session{}, err
	}
	result, err := gw.ResumeSession(ctx, kernel.ResumeSessionRequest{
		AppName:    d.stack.AppName,
		UserID:     d.stack.UserID,
		Workspace:  d.stack.Workspace,
		SessionID:  strings.TrimSpace(sessionID),
		BindingKey: d.bindingKey,
		Binding: kernel.BindingDescriptor{
			Surface: d.bindingKey,
			Owner:   d.stack.AppName,
		},
	})
	if err != nil {
		return session.Session{}, err
	}
	d.mu.Lock()
	d.session = result.Session
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, result.Session)
	return result.Session, nil
}

func (d *GatewayDriver) ListSessions(ctx context.Context, limit int) ([]ResumeCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	ctx, cancel := completionContext(ctx, resumeCompletionTimeout)
	defer cancel()
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	result, err := gw.ListSessions(ctx, kernel.ListSessionsRequest{
		AppName:      d.stack.AppName,
		UserID:       d.stack.UserID,
		WorkspaceKey: d.stack.Workspace.Key,
		Limit:        limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ResumeCandidate, 0, len(result.Sessions))
	for _, session := range result.Sessions {
		candidate := enrichResumeCandidate(ctx, d.stack.Sessions, session)
		if strings.TrimSpace(candidate.Prompt) == "" && strings.TrimSpace(candidate.Title) == "" {
			continue
		}
		out = append(out, candidate)
	}
	return out, nil
}

func (d *GatewayDriver) ReplayEvents(ctx context.Context) ([]kernel.EventEnvelope, error) {
	activeSession, ok := d.currentSession()
	if !ok {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: no active session")
	}
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	result, err := gw.ReplayEvents(ctx, kernel.ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: d.bindingKey,
	})
	if err != nil {
		return nil, err
	}
	return result.Events, nil
}

func (d *GatewayDriver) Compact(ctx context.Context) error {
	activeSession, ok := d.currentSession()
	if !ok {
		return fmt.Errorf("surfaces/tui/gatewaydriver: no active session")
	}
	return d.stack.CompactSession(ctx, activeSession.SessionRef)
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
	gw, err := d.gateway()
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	status.SessionID = activeSession.SessionID
	status.ControllerKind = string(state.Controller.Kind)
	status.ControllerLabel = strings.TrimSpace(firstNonEmpty(state.Controller.AgentName, state.Controller.Label, state.Controller.ControllerID, string(state.Controller.Kind)))
	status.ControllerEpoch = strings.TrimSpace(state.Controller.EpochID)
	status.HasActiveTurn = state.HasActiveTurn
	if kind, ok := activeTurnKindForSession(gw.ActiveTurns(), activeSession.SessionRef); ok {
		status.HasActiveTurn = true
		status.ActiveTurnKind = kind
	}
	if state.Controller.Kind == session.ControllerKindACP {
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
		if participant.Kind == session.ParticipantKindSubagent && participant.Role == session.ParticipantRoleDelegated {
			status.DelegatedParticipants = append(status.DelegatedParticipants, snapshot)
			continue
		}
		status.Participants = append(status.Participants, snapshot)
	}
	return status, nil
}

func agentParticipantSnapshot(participant kernel.ParticipantState) AgentParticipantSnapshot {
	return AgentParticipantSnapshot{
		ID:        strings.TrimSpace(participant.ID),
		Label:     strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID)),
		AgentName: strings.TrimSpace(firstNonEmpty(participant.AgentName, participant.Label, participant.ID)),
		Kind:      string(participant.Kind),
		Role:      string(participant.Role),
		SessionID: strings.TrimSpace(participant.SessionID),
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
	participantID, err := d.resolveParticipantID(ctx, activeSession.SessionRef, handle)
	if err != nil {
		return nil, err
	}
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	result, err := gw.PromptParticipant(ctx, kernel.PromptParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		BindingKey:    d.bindingKey,
		ParticipantID: participantID,
		Input:         prompt,
		ContentParts:  contentParts,
		Source:        "user_side_agent",
	})
	if err != nil {
		return nil, err
	}
	if result.Handle == nil {
		return nil, nil
	}
	return gatewayTurn{handle: result.Handle}, nil
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

type gatewayTurn struct {
	handle kernel.TurnHandle
}

func (t gatewayTurn) HandleID() string               { return t.handle.HandleID() }
func (t gatewayTurn) RunID() string                  { return t.handle.RunID() }
func (t gatewayTurn) TurnID() string                 { return t.handle.TurnID() }
func (t gatewayTurn) SessionRef() session.SessionRef { return t.handle.SessionRef() }
func (t gatewayTurn) Events() <-chan kernel.EventEnvelope {
	return t.handle.Events()
}
func (t gatewayTurn) Submit(ctx context.Context, req kernel.SubmitRequest) error {
	return t.handle.Submit(ctx, req)
}
func (t gatewayTurn) Cancel() kernel.CancelResult { return t.handle.Cancel() }
func (t gatewayTurn) Close() error                { return t.handle.Close() }

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

func (d *GatewayDriver) defaultDisplays() (string, string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.defaultModelText, d.defaultSessionMode, d.defaultSandboxType
}

func (d *GatewayDriver) refreshSessionDisplay(ctx context.Context, activeSession session.Session) {
	if d == nil || d.stack == nil {
		return
	}
	modelText, sessionMode, sandboxType := d.defaultDisplays()
	if alias := strings.TrimSpace(d.stack.DefaultModelAlias()); alias != "" {
		modelText = alias
	}
	if state, err := d.stack.SessionRuntimeState(ctx, activeSession.SessionRef); err == nil {
		if strings.TrimSpace(state.ModelAlias) != "" {
			modelText = strings.TrimSpace(state.ModelAlias)
		}
		if strings.TrimSpace(state.SessionMode) != "" {
			sessionMode = strings.TrimSpace(state.SessionMode)
		}
	}
	d.mu.Lock()
	d.modelText = modelText
	d.sessionMode = sessionMode
	d.sandboxType = sandboxType
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
