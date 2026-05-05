package runtime

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdkcontroller "github.com/OnslaughtSnail/caelis/sdk/controller"
	modelcatalog "github.com/OnslaughtSnail/caelis/sdk/model/catalog"
	sdkproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkskill "github.com/OnslaughtSnail/caelis/sdk/skill"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
)

type GatewayDriver struct {
	mu                  sync.Mutex
	stack               *DriverStack
	session             sdksession.Session
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
	streamParents       map[string]terminalStreamParent
}

type terminalStreamParent struct {
	CallID   string
	ToolName string
	RawInput map[string]any
}

func NewGatewayDriver(ctx context.Context, stack *DriverStack, preferredSessionID string, bindingKey string, modelText string) (*GatewayDriver, error) {
	if stack == nil {
		return nil, fmt.Errorf("tui/runtime: stack is required")
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
		defaultSessionMode:  "default",
		sessionMode:         "default",
		defaultSandboxType:  firstNonEmpty(stack.SandboxStatus().ResolvedBackend, stack.SandboxStatus().RequestedBackend, "auto"),
		sandboxType:         firstNonEmpty(stack.SandboxStatus().ResolvedBackend, stack.SandboxStatus().RequestedBackend, "auto"),
		streamSubscriptions: map[string]struct{}{},
		streamParents:       map[string]terminalStreamParent{},
	}
	if preferredSessionID = strings.TrimSpace(preferredSessionID); preferredSessionID != "" {
		session, err := driver.stack.StartSession(ctx, preferredSessionID, driver.bindingKey)
		if err != nil {
			return nil, err
		}
		driver.session = session
		driver.hasSession = true
		driver.refreshSessionDisplay(ctx, session)
	}
	return driver, nil
}

func (d *GatewayDriver) SubscribeStream(ctx context.Context, env appgateway.EventEnvelope) (<-chan appgateway.EventEnvelope, bool) {
	if d == nil || d.stack == nil || d.stack.Gateway == nil {
		return nil, false
	}
	req, ok := appgateway.StreamRequestFromEvent(env)
	if !ok {
		return nil, false
	}
	d.bindTerminalStreamRequest(&req)
	streams := d.stack.Gateway.Streams()
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

	out := make(chan appgateway.EventEnvelope, 32)
	go func() {
		defer close(out)
		defer func() {
			d.mu.Lock()
			delete(d.streamSubscriptions, key)
			d.mu.Unlock()
		}()
		for frame, err := range streams.Subscribe(ctx, sdkstream.SubscribeRequest{Ref: req.Ref, Cursor: req.Cursor}) {
			if err != nil || frame == nil {
				return
			}
			if frame.Text == "" && frame.Event == nil && !frame.Closed {
				continue
			}
			for _, env := range appgateway.StreamFrameEvents(req, sdkstream.CloneFrame(*frame)) {
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

func (d *GatewayDriver) bindTerminalStreamRequest(req *appgateway.StreamRequest) {
	if d == nil || req == nil {
		return
	}
	toolName := strings.ToUpper(strings.TrimSpace(req.ToolName))
	switch toolName {
	case "SPAWN":
		parent := terminalStreamParent{
			CallID:   strings.TrimSpace(req.CallID),
			ToolName: strings.TrimSpace(req.ToolName),
			RawInput: maps.Clone(req.RawInput),
		}
		if parent.ToolName == "" {
			parent.ToolName = "SPAWN"
		}
		d.mu.Lock()
		if d.streamParents == nil {
			d.streamParents = map[string]terminalStreamParent{}
		}
		for _, key := range terminalStreamParentKeys(*req) {
			d.streamParents[key] = parent
		}
		d.mu.Unlock()
	case "TASK":
		d.mu.Lock()
		parent, ok := d.lookupTerminalStreamParentLocked(*req)
		d.mu.Unlock()
		if !ok {
			return
		}
		req.CallID = parent.CallID
		req.ToolName = firstNonEmpty(parent.ToolName, "SPAWN")
		req.RawInput = taskContinuationRawInput(parent.RawInput, req.RawInput, req.Ref.TaskID)
	}
}

func (d *GatewayDriver) lookupTerminalStreamParentLocked(req appgateway.StreamRequest) (terminalStreamParent, bool) {
	if d == nil || len(d.streamParents) == 0 {
		return terminalStreamParent{}, false
	}
	for _, key := range terminalStreamParentKeys(req) {
		if parent, ok := d.streamParents[key]; ok && strings.TrimSpace(parent.CallID) != "" {
			return terminalStreamParent{
				CallID:   parent.CallID,
				ToolName: parent.ToolName,
				RawInput: maps.Clone(parent.RawInput),
			}, true
		}
	}
	return terminalStreamParent{}, false
}

func terminalStreamParentKeys(req appgateway.StreamRequest) []string {
	sessionID := strings.TrimSpace(req.SessionRef.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(req.Ref.SessionID)
	}
	keys := make([]string, 0, 2)
	if taskID := strings.TrimSpace(req.Ref.TaskID); taskID != "" {
		keys = append(keys, strings.Join([]string{sessionID, "task", taskID}, "\x00"))
	}
	if terminalID := strings.TrimSpace(req.Ref.TerminalID); terminalID != "" {
		keys = append(keys, strings.Join([]string{sessionID, "terminal", terminalID}, "\x00"))
	}
	return keys
}

func taskContinuationRawInput(parent map[string]any, task map[string]any, taskID string) map[string]any {
	out := maps.Clone(parent)
	if out == nil {
		out = map[string]any{}
	}
	if action := strings.ToLower(strings.TrimSpace(anyString(task["action"]))); action == "write" {
		if prompt := strings.TrimSpace(anyString(task["input"])); prompt != "" {
			out["prompt"] = prompt
		}
	}
	if visibleTaskID := strings.TrimSpace(firstNonEmpty(anyString(task["task_id"]), taskID)); visibleTaskID != "" {
		out["task_id"] = visibleTaskID
	}
	return out
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

func (d *GatewayDriver) ensureSession(ctx context.Context) (sdksession.Session, error) {
	if session, ok := d.currentSession(); ok {
		return session, nil
	}
	if d == nil || d.stack == nil {
		return sdksession.Session{}, fmt.Errorf("tui/runtime: stack is unavailable")
	}
	session, err := d.stack.StartSession(ctx, "", d.bindingKey)
	if err != nil {
		return sdksession.Session{}, err
	}
	d.mu.Lock()
	d.session = session
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, session)
	return session, nil
}

func (d *GatewayDriver) currentSession() (sdksession.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasSession {
		return sdksession.Session{}, false
	}
	return d.session, true
}

func (d *GatewayDriver) activeACPControllerStatus(ctx context.Context) (sdkcontroller.ControllerStatus, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.stack == nil {
		return sdkcontroller.ControllerStatus{}, false, nil
	}
	session, ok := d.currentSession()
	if !ok || session.Controller.Kind != sdksession.ControllerKindACP {
		return sdkcontroller.ControllerStatus{}, false, nil
	}
	status, found, err := d.stack.ACPControllerStatus(ctx, session.SessionRef)
	if err != nil {
		return sdkcontroller.ControllerStatus{}, false, err
	}
	if !found {
		status = sdkcontroller.ControllerStatus{
			SessionRef:      session.SessionRef,
			Agent:           firstNonEmpty(strings.TrimSpace(session.Controller.AgentName), strings.TrimSpace(session.Controller.Label), strings.TrimSpace(session.Controller.ControllerID)),
			RemoteSessionID: strings.TrimSpace(session.Controller.RemoteSessionID),
		}
	}
	return status, true, nil
}

func (d *GatewayDriver) Status(ctx context.Context) (StatusSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	modelText, sessionMode, sandboxType := d.defaultDisplays()
	reasoningEffort := ""
	if d.stack != nil {
		if alias := strings.TrimSpace(d.stack.DefaultModelAlias()); alias != "" {
			modelText = alias
		}
	}
	sandboxStatus := SandboxStatus{}
	if d.stack != nil {
		sandboxStatus = d.stack.SandboxStatus()
	}
	session, ok := d.currentSession()
	if ok && d.stack != nil {
		if state, err := d.stack.SessionRuntimeState(context.Background(), session.SessionRef); err == nil {
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
		acpModelText = acpControllerModelText(acpStatus, session)
		modelText = acpModelText
		reasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
		acpModeID = strings.TrimSpace(acpStatus.Mode)
		acpModeLabel = acpControllerModeDisplay(acpStatus)
	}
	sandboxType = firstNonEmpty(sandboxStatus.ResolvedBackend, sandboxStatus.RequestedBackend, sandboxType)
	route := sandboxStatus.Route
	securitySummary := sandboxStatus.SecuritySummary
	if !activeACP && strings.EqualFold(strings.TrimSpace(sessionMode), "full_access") {
		route = "host"
		securitySummary = "full access"
	}
	d.mu.Lock()
	sessionID := ""
	if ok {
		sessionID = session.SessionID
	}
	liveModelText := d.modelText
	liveSessionMode := d.sessionMode
	liveSandboxType := d.sandboxType
	bindingKey := d.bindingKey
	d.mu.Unlock()
	rawModelText := firstNonEmpty(modelText, liveModelText)

	status := StatusSnapshot{
		SessionID:               sessionID,
		Workspace:               strings.TrimSpace(d.stack.Workspace.CWD),
		Model:                   formatReasoningModelDisplay(rawModelText, reasoningEffort),
		ReasoningEffort:         reasoningEffort,
		ModeLabel:               firstNonEmpty(sessionMode, liveSessionMode),
		SessionMode:             firstNonEmpty(sessionMode, liveSessionMode),
		SandboxType:             firstNonEmpty(sandboxType, liveSandboxType),
		SandboxRequestedBackend: firstNonEmpty(sandboxStatus.RequestedBackend, "auto"),
		SandboxResolvedBackend:  firstNonEmpty(sandboxStatus.ResolvedBackend, sandboxStatus.RequestedBackend, liveSandboxType),
		Route:                   route,
		FallbackReason:          sandboxStatus.FallbackReason,
		SecuritySummary:         securitySummary,
		HostExecution:           strings.EqualFold(strings.TrimSpace(route), "host"),
		FullAccessMode:          strings.EqualFold(strings.TrimSpace(sessionMode), "full_access"),
		Surface:                 bindingKey,
	}
	if d.stack != nil {
		req := DoctorRequest{}
		if ok {
			req.SessionRef = session.SessionRef
		}
		if report, err := d.stack.Doctor(context.Background(), req); err == nil {
			status.StoreDir = strings.TrimSpace(report.StoreDir)
			status.Provider = strings.TrimSpace(report.ActiveProvider)
			status.ModelName = strings.TrimSpace(report.ActiveModel)
			status.MissingAPIKey = report.MissingAPIKey
			status.HostExecution = report.HostExecution
			status.FullAccessMode = report.FullAccessMode
			status.SandboxRequestedBackend = firstNonEmpty(strings.TrimSpace(report.SandboxRequestedBackend), status.SandboxRequestedBackend)
			status.SandboxResolvedBackend = firstNonEmpty(strings.TrimSpace(report.SandboxResolvedBackend), status.SandboxResolvedBackend)
			status.Route = firstNonEmpty(strings.TrimSpace(report.SandboxRoute), status.Route)
			status.FallbackReason = firstNonEmpty(strings.TrimSpace(report.SandboxFallbackReason), status.FallbackReason)
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
		if status.ReasoningEffort == "" {
			if activeACP {
				status.ReasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
				status.Model = formatReasoningModelDisplay(firstNonEmpty(strings.TrimSpace(acpStatus.Model), rawModelText), status.ReasoningEffort)
			} else if cfg, ok := d.stack.ModelConfig(rawModelText); ok {
				status.ReasoningEffort = firstNonEmpty(cfg.ReasoningEffort, cfg.DefaultReasoningEffort)
				status.Model = formatReasoningModelDisplay(rawModelText, status.ReasoningEffort)
			}
		}
		if ok && !activeACP {
			if usage, err := d.stack.SessionUsageSnapshot(context.Background(), session.SessionRef, rawModelText); err == nil {
				status.TotalTokens = usage.TotalTokens
				status.ContextWindowTokens = usage.ContextWindowTokens
			}
		}
		if ok {
			if usage, err := d.sessionTokenUsage(context.Background(), session.SessionRef); err == nil {
				status.SessionInputTokens = usage.PromptTokens
				status.SessionCachedInputTokens = usage.CachedInputTokens
				status.SessionOutputTokens = usage.CompletionTokens
				status.SessionTotalTokens = usage.TotalTokens
			}
		}
	}
	if activeACP {
		rawModelText = firstNonEmpty(strings.TrimSpace(acpStatus.Model), acpModelText, rawModelText)
		status.Model = formatReasoningModelDisplay(rawModelText, strings.TrimSpace(acpStatus.ReasoningEffort))
		status.ReasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
		status.SessionMode = acpModeID
		status.ModeLabel = firstNonEmpty(acpModeLabel, acpModeID)
		status.Provider = "acp"
		status.ModelName = strings.TrimSpace(acpStatus.Model)
		status.MissingAPIKey = false
		status.FullAccessMode = strings.EqualFold(acpModeID, "full_access")
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
	return status, nil
}

func (d *GatewayDriver) sessionTokenUsage(ctx context.Context, ref sdksession.SessionRef) (appgateway.UsageSnapshot, error) {
	if d == nil || d.stack == nil || d.stack.Sessions == nil {
		return appgateway.UsageSnapshot{}, nil
	}
	if strings.TrimSpace(ref.SessionID) == "" {
		return appgateway.UsageSnapshot{}, nil
	}
	events, err := d.stack.Sessions.Events(ctx, sdksession.EventsRequest{SessionRef: ref})
	if err != nil {
		return appgateway.UsageSnapshot{}, err
	}
	var usage appgateway.UsageSnapshot
	lastToolCallUsageKey := ""
	lastUsageWasToolCall := false
	for _, event := range events {
		one := appgateway.UsageSnapshotFromSessionEvent(event)
		if one == nil {
			if sdksession.EventTypeOf(event) != sdksession.EventTypeToolCall {
				lastToolCallUsageKey = ""
				lastUsageWasToolCall = false
			}
			continue
		}
		isToolCall := sdksession.EventTypeOf(event) == sdksession.EventTypeToolCall
		usageKey := usageSnapshotDedupeKey(*one)
		if isToolCall && lastUsageWasToolCall && usageKey != "" && usageKey == lastToolCallUsageKey {
			continue
		}
		usage.PromptTokens += one.PromptTokens
		usage.CachedInputTokens += one.CachedInputTokens
		usage.CompletionTokens += one.CompletionTokens
		usage.TotalTokens += one.TotalTokens
		if isToolCall {
			lastToolCallUsageKey = usageKey
			lastUsageWasToolCall = true
		} else {
			lastToolCallUsageKey = ""
			lastUsageWasToolCall = false
		}
	}
	return usage, nil
}

func usageSnapshotDedupeKey(usage appgateway.UsageSnapshot) string {
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d/%d/%d", usage.PromptTokens, usage.CachedInputTokens, usage.CompletionTokens, usage.TotalTokens)
}

func (d *GatewayDriver) Submit(ctx context.Context, submission Submission) (Turn, error) {
	session, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	result, err := d.stack.Gateway.BeginTurn(ctx, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      strings.TrimSpace(submission.Text),
		Surface:    d.bindingKey,
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

func (d *GatewayDriver) Interrupt(ctx context.Context) error {
	cancelCommand := d.activeCommandInterrupt()
	if cancelCommand != nil {
		cancelCommand()
	}
	session, ok := d.currentSession()
	if !ok {
		if cancelCommand != nil {
			return nil
		}
		return fmt.Errorf("tui/runtime: no active session")
	}
	if err := d.stack.Gateway.Interrupt(ctx, appgateway.InterruptRequest{
		SessionRef: session.SessionRef,
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

func (d *GatewayDriver) NewSession(ctx context.Context) (sdksession.Session, error) {
	session, err := d.stack.StartSession(ctx, "", d.bindingKey)
	if err != nil {
		return sdksession.Session{}, err
	}
	d.mu.Lock()
	d.session = session
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, session)
	return session, nil
}

func (d *GatewayDriver) ResumeSession(ctx context.Context, sessionID string) (sdksession.Session, error) {
	result, err := d.stack.Gateway.ResumeSession(ctx, appgateway.ResumeSessionRequest{
		AppName:    d.stack.AppName,
		UserID:     d.stack.UserID,
		Workspace:  d.stack.Workspace,
		SessionID:  strings.TrimSpace(sessionID),
		BindingKey: d.bindingKey,
		Binding: appgateway.BindingDescriptor{
			Surface: d.bindingKey,
			Owner:   d.stack.AppName,
		},
	})
	if err != nil {
		return sdksession.Session{}, err
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
	result, err := d.stack.Gateway.ListSessions(ctx, appgateway.ListSessionsRequest{
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

func (d *GatewayDriver) ReplayEvents(ctx context.Context) ([]appgateway.EventEnvelope, error) {
	session, ok := d.currentSession()
	if !ok {
		return nil, fmt.Errorf("tui/runtime: no active session")
	}
	result, err := d.stack.Gateway.ReplayEvents(ctx, appgateway.ReplayEventsRequest{
		SessionRef: session.SessionRef,
		BindingKey: d.bindingKey,
	})
	if err != nil {
		return nil, err
	}
	return result.Events, nil
}

func (d *GatewayDriver) Compact(ctx context.Context) error {
	session, ok := d.currentSession()
	if !ok {
		return fmt.Errorf("tui/runtime: no active session")
	}
	return d.stack.CompactSession(ctx, session.SessionRef)
}

func (d *GatewayDriver) Connect(ctx context.Context, cfg ConnectConfig) (StatusSnapshot, error) {
	tpl, ok := findProviderTemplate(cfg.Provider)
	if !ok {
		return StatusSnapshot{}, fmt.Errorf("provider %q is not supported", strings.TrimSpace(cfg.Provider))
	}
	cfg.Provider = tpl.provider
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.TokenEnv = strings.TrimSpace(cfg.TokenEnv)
	if env, ok := parseTokenEnvSpec(cfg.APIKey); ok {
		cfg.TokenEnv = env
		cfg.APIKey = ""
	}
	if err := validateConnectConfig(tpl, cfg); err != nil {
		return StatusSnapshot{}, err
	}
	if defaults, err := connectDefaultsForConfig(ctx, cfg); err == nil {
		if cfg.ContextWindowTokens <= 0 {
			cfg.ContextWindowTokens = defaults.ContextWindow
		}
		if cfg.MaxOutputTokens <= 0 {
			cfg.MaxOutputTokens = defaults.MaxOutput
		}
		if len(cfg.ReasoningLevels) == 0 {
			cfg.ReasoningLevels = defaults.ReasoningLevels
		}
		if cfg.ReasoningEffort == "" {
			cfg.ReasoningEffort = defaults.DefaultReasoningEffort
		}
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = tpl.defaultBaseURL
	}
	if tpl.provider == "codefree" {
		if _, err := sdkproviders.CodeFreeEnsureAuth(ctx, sdkproviders.CodeFreeEnsureAuthOptions{
			BaseURL:         baseURL,
			OpenBrowser:     true,
			CallbackTimeout: 5 * time.Minute,
		}); err != nil {
			return StatusSnapshot{}, err
		}
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if cfg.TimeoutSeconds <= 0 {
		timeout = 60 * time.Second
	}
	authType := authTypeFromString(strings.TrimSpace(cfg.AuthType))
	if tpl.noAuthRequired {
		authType = sdkproviders.AuthNone
	}
	persistToken := strings.TrimSpace(cfg.APIKey) != "" && strings.TrimSpace(cfg.TokenEnv) == ""
	reasoningLevels := normalizeReasoningLevels(cfg.ReasoningLevels)
	defaultReasoningEffort := strings.TrimSpace(cfg.ReasoningEffort)
	alias, err := d.stack.Connect(ModelConfig{
		Provider:               strings.TrimSpace(cfg.Provider),
		API:                    tpl.api,
		Model:                  cfg.Model,
		BaseURL:                baseURL,
		Token:                  cfg.APIKey,
		TokenEnv:               cfg.TokenEnv,
		PersistToken:           persistToken,
		AuthType:               authType,
		ContextWindowTokens:    cfg.ContextWindowTokens,
		DefaultReasoningEffort: defaultReasoningEffort,
		ReasoningEffort:        defaultReasoningEffort,
		ReasoningLevels:        reasoningLevels,
		MaxOutputTok:           cfg.MaxOutputTokens,
		Timeout:                timeout,
	})
	if err != nil {
		return StatusSnapshot{}, err
	}
	if session, ok := d.currentSession(); ok && alias != "" {
		if err := d.stack.UseModel(ctx, session.SessionRef, alias); err != nil {
			return StatusSnapshot{}, err
		}
	}
	d.mu.Lock()
	if alias != "" {
		d.defaultModelText = alias
		d.modelText = alias
	}
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) UseModel(ctx context.Context, model string, reasoningEffort ...string) (StatusSnapshot, error) {
	session, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if _, activeACP, err := d.activeACPControllerStatus(ctx); err != nil {
		return StatusSnapshot{}, err
	} else if activeACP {
		reasoning := ""
		if len(reasoningEffort) > 0 {
			reasoning = strings.TrimSpace(reasoningEffort[0])
		}
		status, err := d.stack.SetACPControllerModel(ctx, session.SessionRef, strings.TrimSpace(model), reasoning)
		if err != nil {
			return StatusSnapshot{}, err
		}
		d.mu.Lock()
		d.modelText = strings.TrimSpace(firstNonEmpty(status.Model, model))
		d.mu.Unlock()
		return d.Status(ctx)
	}
	alias, err := d.resolveStoredModelAlias(ctx, strings.TrimSpace(model))
	if err != nil {
		return StatusSnapshot{}, err
	}
	if alias == "" {
		return StatusSnapshot{}, fmt.Errorf("tui/runtime: model alias is required")
	}
	reasoning := ""
	if len(reasoningEffort) > 0 {
		reasoning = strings.TrimSpace(reasoningEffort[0])
		if reasoning != "" && !d.modelAliasSupportsReasoningLevel(alias, reasoning) {
			return StatusSnapshot{}, fmt.Errorf("tui/runtime: model %q does not support reasoning level %q", alias, reasoning)
		}
	}
	if err := d.stack.UseModel(ctx, session.SessionRef, alias, reasoning); err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.modelText = alias
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) DeleteModel(ctx context.Context, alias string) error {
	session, err := d.ensureSession(ctx)
	if err != nil {
		return err
	}
	resolved, err := d.resolveStoredModelAlias(ctx, strings.TrimSpace(alias))
	if err != nil {
		return err
	}
	if err := d.stack.DeleteModel(ctx, session.SessionRef, resolved); err != nil {
		return err
	}
	d.mu.Lock()
	d.defaultModelText = strings.TrimSpace(d.stack.DefaultModelAlias())
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, session)
	return nil
}

func (d *GatewayDriver) CycleSessionMode(ctx context.Context) (StatusSnapshot, error) {
	session, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if acpStatus, activeACP, err := d.activeACPControllerStatus(ctx); err != nil {
		return StatusSnapshot{}, err
	} else if activeACP {
		next, err := nextACPControllerMode(acpStatus)
		if err != nil {
			return StatusSnapshot{}, err
		}
		status, err := d.stack.SetACPControllerMode(ctx, session.SessionRef, next.ID)
		if err != nil {
			return StatusSnapshot{}, err
		}
		d.mu.Lock()
		d.sessionMode = strings.TrimSpace(firstNonEmpty(status.Mode, next.ID))
		d.mu.Unlock()
		return d.Status(ctx)
	}
	normalized, err := d.stack.CycleSessionMode(ctx, session.SessionRef)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sessionMode = normalized
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) SetSandboxBackend(ctx context.Context, backend string) (StatusSnapshot, error) {
	status, err := d.stack.SetSandboxBackend(ctx, backend)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sandboxType = firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, d.sandboxType)
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) SetSandboxMode(ctx context.Context, mode string) (StatusSnapshot, error) {
	session, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if _, activeACP, err := d.activeACPControllerStatus(ctx); err != nil {
		return StatusSnapshot{}, err
	} else if activeACP {
		status, err := d.stack.SetACPControllerMode(ctx, session.SessionRef, strings.TrimSpace(mode))
		if err != nil {
			return StatusSnapshot{}, err
		}
		d.mu.Lock()
		d.sessionMode = strings.TrimSpace(status.Mode)
		d.mu.Unlock()
		return d.Status(ctx)
	}
	normalized, err := d.stack.SetSessionMode(ctx, session.SessionRef, mode)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sessionMode = normalized
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) ListAgents(ctx context.Context, limit int) ([]AgentCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	return d.agentCatalog(limit), nil
}

func (d *GatewayDriver) AgentStatus(ctx context.Context) (AgentStatusSnapshot, error) {
	status := AgentStatusSnapshot{
		AvailableAgents: d.agentCatalog(0),
	}
	session, ok := d.currentSession()
	if !ok {
		return status, nil
	}
	state, err := d.stack.Gateway.ControlPlaneState(ctx, appgateway.ControlPlaneStateRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	status.SessionID = session.SessionID
	status.ControllerKind = string(state.Controller.Kind)
	status.ControllerLabel = strings.TrimSpace(firstNonEmpty(state.Controller.AgentName, state.Controller.Label, state.Controller.ControllerID, string(state.Controller.Kind)))
	status.ControllerEpoch = strings.TrimSpace(state.Controller.EpochID)
	status.HasActiveTurn = state.HasActiveTurn
	if state.Controller.Kind == sdksession.ControllerKindACP {
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
	for _, participant := range state.Participants {
		if participant.Kind == sdksession.ParticipantKindSubagent && participant.Role == sdksession.ParticipantRoleDelegated {
			continue
		}
		if shouldHideSelfDelegatedParticipant(participant) {
			continue
		}
		status.Participants = append(status.Participants, AgentParticipantSnapshot{
			ID:        strings.TrimSpace(participant.ID),
			Label:     strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID)),
			AgentName: strings.TrimSpace(firstNonEmpty(participant.AgentName, participant.Label, participant.ID)),
			Kind:      string(participant.Kind),
			Role:      string(participant.Role),
			SessionID: strings.TrimSpace(participant.SessionID),
		})
	}
	return status, nil
}

func (d *GatewayDriver) AddAgent(ctx context.Context, target string) (AgentStatusSnapshot, error) {
	return d.AddAgentWithOptions(ctx, target, AgentAddOptions{})
}

func (d *GatewayDriver) AddAgentWithOptions(ctx context.Context, target string, opts AgentAddOptions) (AgentStatusSnapshot, error) {
	if opts.Install {
		var finish func()
		ctx, finish = d.beginInterruptibleCommand(ctx)
		defer finish()
	}
	if err := d.stack.RegisterBuiltinACPAgentWithOptions(ctx, target, RegisterBuiltinACPAgentOptions{
		Install: opts.Install,
	}); err != nil {
		if opts.Install && errors.Is(ctx.Err(), context.Canceled) {
			return AgentStatusSnapshot{}, context.Canceled
		}
		return AgentStatusSnapshot{}, err
	}
	return d.AgentStatus(ctx)
}

func (d *GatewayDriver) RemoveAgent(ctx context.Context, target string) (AgentStatusSnapshot, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	status, err := d.AgentStatus(ctx)
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	if strings.EqualFold(strings.TrimSpace(status.ControllerKind), string(sdksession.ControllerKindACP)) {
		return AgentStatusSnapshot{}, fmt.Errorf("tui/runtime: an ACP agent is the active controller; run /agent use local before removing registered agents")
	}
	if err := d.stack.UnregisterACPAgent(target); err != nil {
		return AgentStatusSnapshot{}, err
	}
	return d.AgentStatus(ctx)
}

func (d *GatewayDriver) HandoffAgent(ctx context.Context, target string) (AgentStatusSnapshot, error) {
	session, err := d.ensureSession(ctx)
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	target = strings.TrimSpace(target)
	req := appgateway.HandoffControllerRequest{
		SessionRef: session.SessionRef,
		BindingKey: d.bindingKey,
		Source:     "tui_agent_handoff",
	}
	switch strings.ToLower(target) {
	case "", "main", "local", "kernel":
		req.Kind = sdksession.ControllerKindKernel
		req.Reason = "resume local control"
	default:
		agent, resolveErr := d.resolveAgentName(target)
		if resolveErr != nil {
			return AgentStatusSnapshot{}, resolveErr
		}
		req.Kind = sdksession.ControllerKindACP
		req.Agent = agent
		req.Reason = "handoff to agent"
	}
	updated, err := d.stack.Gateway.HandoffController(ctx, req)
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	d.mu.Lock()
	d.session = updated
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, updated)
	return d.AgentStatus(ctx)
}

func (d *GatewayDriver) StartAgentSubagent(ctx context.Context, target string, prompt string) (Turn, error) {
	session, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	agent, err := d.resolveAgentName(target)
	if err != nil {
		return nil, err
	}
	label := d.allocateSideAgentLabel(ctx, session.SessionRef, agent)
	updated, err := d.stack.Gateway.AttachParticipant(ctx, appgateway.AttachParticipantRequest{
		SessionRef: session.SessionRef,
		BindingKey: d.bindingKey,
		Agent:      agent,
		Role:       sdksession.ParticipantRoleSidecar,
		Source:     "slash_" + agent,
		Label:      label,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.session = updated
	d.hasSession = true
	d.mu.Unlock()
	participantID, err := sideAgentParticipantID(updated, agent, label)
	if err != nil {
		if rollbackErr := d.detachSideAgentAfterPromptFailure(ctx, updated.SessionRef, participantID); rollbackErr != nil {
			return nil, errors.Join(err, rollbackErr)
		}
		return nil, err
	}
	result, err := d.stack.Gateway.PromptParticipant(ctx, appgateway.PromptParticipantRequest{
		SessionRef:    updated.SessionRef,
		BindingKey:    d.bindingKey,
		ParticipantID: participantID,
		Input:         strings.TrimSpace(prompt),
		Source:        "slash_" + agent,
	})
	if err != nil {
		if rollbackErr := d.detachSideAgentAfterPromptFailure(ctx, updated.SessionRef, participantID); rollbackErr != nil {
			return nil, errors.Join(err, rollbackErr)
		}
		return nil, err
	}
	if result.Handle == nil {
		return nil, nil
	}
	return gatewayTurn{handle: result.Handle}, nil
}

func (d *GatewayDriver) detachSideAgentAfterPromptFailure(ctx context.Context, ref sdksession.SessionRef, participantID string) error {
	participantID = strings.TrimSpace(participantID)
	if participantID == "" || d == nil || d.stack == nil || d.stack.Gateway == nil {
		return nil
	}
	updated, err := d.stack.Gateway.DetachParticipant(context.WithoutCancel(ctx), appgateway.DetachParticipantRequest{
		SessionRef:    ref,
		BindingKey:    d.bindingKey,
		ParticipantID: participantID,
		Source:        "side_agent_prompt_rollback",
	})
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.session = updated
	d.hasSession = true
	d.mu.Unlock()
	return nil
}

var sideAgentHandleNames = []string{
	"jeff", "omna", "amy", "mike", "luna", "leo", "emma", "zoe",
	"liam", "maya", "nora", "jack", "iris", "kate", "alex", "ella",
	"owen", "ruby", "evan", "noah", "mia", "lucy", "jude", "cole",
	"claire",
}

func (d *GatewayDriver) allocateSideAgentLabel(ctx context.Context, ref sdksession.SessionRef, agent string) string {
	used := map[string]struct{}{}
	if d != nil && d.stack != nil && d.stack.Gateway != nil {
		if state, err := d.stack.Gateway.ControlPlaneState(ctx, appgateway.ControlPlaneStateRequest{SessionRef: ref}); err == nil {
			for _, participant := range state.Participants {
				label := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(participant.Label), "@"))
				if label != "" {
					used[label] = struct{}{}
				}
			}
		}
	}
	return "@" + allocateSideAgentHandle(used, agent)
}

func allocateSideAgentHandle(used map[string]struct{}, agent string) string {
	for _, candidate := range sideAgentHandleNames {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
	base := strings.ToLower(strings.TrimSpace(agent))
	if base == "" {
		base = "agent"
	}
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s%d", base, i+1)
		}
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
	return base
}

func sideAgentParticipantID(session sdksession.Session, agent string, label string) (string, error) {
	agent = strings.TrimSpace(agent)
	label = strings.TrimSpace(label)
	for i := len(session.Participants) - 1; i >= 0; i-- {
		participant := session.Participants[i]
		if participant.Role != sdksession.ParticipantRoleSidecar {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(participant.AgentName), agent) {
			continue
		}
		if label != "" && !strings.EqualFold(strings.TrimSpace(participant.Label), label) {
			continue
		}
		if id := strings.TrimSpace(participant.ID); id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("tui/runtime: side ACP participant %q was not attached", agent)
}

func (d *GatewayDriver) ContinueSubagent(ctx context.Context, handle string, prompt string) (Turn, error) {
	session, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	participantID, err := d.resolveParticipantID(ctx, session.SessionRef, handle)
	if err != nil {
		return nil, err
	}
	result, err := d.stack.Gateway.PromptParticipant(ctx, appgateway.PromptParticipantRequest{
		SessionRef:    session.SessionRef,
		BindingKey:    d.bindingKey,
		ParticipantID: participantID,
		Input:         strings.TrimSpace(prompt),
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

func (d *GatewayDriver) CompleteMention(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	session, ok := d.currentSession()
	if !ok {
		return []CompletionCandidate{}, nil
	}
	state, err := d.stack.Gateway.ControlPlaneState(ctx, appgateway.ControlPlaneStateRequest{SessionRef: session.SessionRef})
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	out := make([]CompletionCandidate, 0, min(limit, len(state.Participants)))
	for _, participant := range state.Participants {
		if !isUserSideParticipant(participant) {
			continue
		}
		handle := strings.TrimPrefix(strings.TrimSpace(participant.Label), "@")
		if handle == "" {
			continue
		}
		agent := strings.TrimSpace(participant.AgentName)
		if agent == "" {
			agent = strings.TrimSpace(participant.ID)
		}
		if query != "" && !hasSlashArgPrefix(query, handle, agent, participant.SessionID, participant.DelegationID) {
			continue
		}
		out = append(out, CompletionCandidate{
			Value:   handle,
			Display: handle,
			Detail:  strings.Join(compactNonEmpty([]string{agent, string(participant.Role), participant.SessionID}), " · "),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func isUserSideParticipant(participant appgateway.ParticipantState) bool {
	if participant.Role != sdksession.ParticipantRoleSidecar {
		return false
	}
	return participant.Kind == sdksession.ParticipantKindACP
}

func shouldHideSelfDelegatedParticipant(participant appgateway.ParticipantState) bool {
	if participant.Kind != sdksession.ParticipantKindSubagent || participant.Role != sdksession.ParticipantRoleDelegated {
		return false
	}
	agent := strings.ToLower(strings.TrimSpace(participant.AgentName))
	id := strings.ToLower(strings.TrimSpace(participant.ID))
	return agent == "self" || strings.HasPrefix(id, "self-")
}

func (d *GatewayDriver) CompleteFile(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	return completeWorkspaceFiles(ctx, d.WorkspaceDir(), query, limit)
}

func (d *GatewayDriver) CompleteSkill(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	limit = normalizeCompletionLimit(limit)

	skills, err := sdkskill.DiscoverMeta(nil, d.WorkspaceDir())
	if err != nil {
		return nil, err
	}
	workspace := d.WorkspaceDir()
	scored := make([]scoredCompletion, 0, len(skills))
	for _, skill := range skills {
		score, ok := scoreSkillMeta(query, skill, workspace)
		if !ok {
			continue
		}
		pathHint := displayPathHint(workspace, skill.Path)
		detail := strings.Join(compactNonEmpty([]string{strings.TrimSpace(skill.Description), pathHint}), " · ")
		scored = append(scored, scoredCompletion{
			candidate: CompletionCandidate{
				Value:   strings.TrimSpace(skill.Name),
				Display: strings.TrimSpace(skill.Name),
				Detail:  strings.TrimSpace(detail),
				Path:    strings.TrimSpace(skill.Path),
			},
			score: score,
		})
	}
	return sortAndTrimCandidates(scored, limit), nil
}

func (d *GatewayDriver) CompleteResume(ctx context.Context, query string, limit int) ([]ResumeCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	all, err := d.ListSessions(ctx, limit)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return all, nil
	}
	out := make([]ResumeCandidate, 0, min(limit, len(all)))
	for _, item := range all {
		if _, ok := scoreResumeCandidate(query, item); !ok {
			continue
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) CompleteSlashArg(ctx context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
	if limit <= 0 {
		limit = 8
	}
	query = strings.TrimSpace(strings.ToLower(query))
	if acpStatus, activeACP, err := d.activeACPControllerStatus(ctx); err != nil {
		return nil, err
	} else if activeACP {
		if candidates, handled := d.completeACPControllerSlashArg(acpStatus, command, query, limit); handled {
			return candidates, nil
		}
	}
	switch strings.TrimSpace(strings.ToLower(command)) {
	case "agent add":
		return d.completeBuiltInAgentCatalog(query, limit), nil
	case "agent install":
		return d.completeInstallableBuiltInAgentCatalog(query, limit), nil
	case "agent add --install":
		return d.completeInstallableBuiltInAgentCatalog(query, limit), nil
	case "agent use":
		return d.completeAgentHandoffTargets(ctx, query, limit)
	case "agent remove":
		return d.completeAgentCatalog(query, limit), nil
	case "model use", "model del":
		return d.completeModelAliases(ctx, query, limit)
	case "connect":
		return completeConnectArgs(ctx, d, "connect", query, limit)
	}
	if strings.HasPrefix(strings.TrimSpace(strings.ToLower(command)), "connect-") {
		return completeConnectArgs(ctx, d, strings.TrimSpace(strings.ToLower(command)), query, limit)
	}
	if alias, ok := strings.CutPrefix(strings.TrimSpace(strings.ToLower(command)), "model use "); ok {
		return d.completeModelReasoningLevels(ctx, alias, query, limit)
	}
	candidates := defaultSlashArgCandidates(strings.TrimSpace(strings.ToLower(command)))
	out := make([]SlashArgCandidate, 0, min(limit, len(candidates)))
	for _, candidate := range candidates {
		if query != "" && !hasSlashArgPrefix(query, candidate.Value, candidate.Display, candidate.Detail) {
			continue
		}
		out = append(out, candidate)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) completeACPControllerSlashArg(status sdkcontroller.ControllerStatus, command string, query string, limit int) ([]SlashArgCandidate, bool) {
	normalized := strings.TrimSpace(strings.ToLower(command))
	switch normalized {
	case "model":
		candidate := SlashArgCandidate{
			Value:   "use",
			Display: "use",
			Detail:  "switch remote ACP model",
		}
		if query != "" && !hasSlashArgPrefix(query, candidate.Value, candidate.Display, candidate.Detail) {
			return nil, true
		}
		return []SlashArgCandidate{candidate}, true
	case "model use":
		return controllerChoicesToSlashCandidates(status.ModelOptions, "remote ACP model", query, limit), true
	case "model del", "model delete", "model rm":
		return nil, true
	}
	if alias, ok := strings.CutPrefix(normalized, "model use "); ok && strings.TrimSpace(alias) != "" {
		efforts := acpControllerEffortsForModel(status, alias)
		return controllerChoicesToSlashCandidates(efforts, "remote ACP reasoning effort", query, limit), true
	}
	return nil, false
}

func (d *GatewayDriver) completeModelReasoningLevels(ctx context.Context, aliasQuery string, query string, limit int) ([]SlashArgCandidate, error) {
	alias, err := d.resolveStoredModelAlias(ctx, aliasQuery)
	if err != nil {
		return nil, nil
	}
	cfg, ok := d.stack.ModelConfig(alias)
	if !ok {
		return nil, nil
	}
	levels := configuredModelReasoningLevels(cfg)
	out := make([]SlashArgCandidate, 0, min(limit, len(levels)))
	for _, level := range levels {
		if query != "" && !hasSlashArgPrefix(query, level) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   level,
			Display: level,
			Detail:  modelReasoningLevelDetail(level),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) modelAliasSupportsReasoningLevel(alias string, level string) bool {
	cfg, ok := d.stack.ModelConfig(alias)
	if !ok {
		return false
	}
	for _, one := range configuredModelReasoningLevels(cfg) {
		if strings.EqualFold(strings.TrimSpace(one), strings.TrimSpace(level)) {
			return true
		}
	}
	return false
}

func configuredModelReasoningLevels(cfg ModelConfig) []string {
	levels := normalizeReasoningLevels(cfg.ReasoningLevels)
	for _, level := range normalizeReasoningLevels(modelcatalog.ReasoningLevelsForModel(cfg.Provider, cfg.Model)) {
		seen := false
		for _, existing := range levels {
			if strings.EqualFold(existing, level) {
				seen = true
				break
			}
		}
		if !seen {
			levels = append(levels, level)
		}
	}
	return levels
}

func modelReasoningLevelDetail(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "none":
		return "reasoning disabled"
	case "high", "medium", "low", "minimal", "xhigh":
		return "reasoning level"
	default:
		return "reasoning option"
	}
}

func controllerCommandNames(commands []sdkcontroller.ControllerCommand) []string {
	if len(commands) == 0 {
		return nil
	}
	out := make([]string, 0, len(commands))
	seen := map[string]struct{}{}
	for _, command := range commands {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(command.Name, "/")))
		if name == "" {
			continue
		}
		if fields := strings.Fields(name); len(fields) > 0 {
			name = fields[0]
		}
		if _, exists := seen[name]; exists {
			continue
		}
		out = append(out, name)
		seen[name] = struct{}{}
	}
	return out
}

func acpControllerModelText(status sdkcontroller.ControllerStatus, session sdksession.Session) string {
	return firstNonEmpty(
		strings.TrimSpace(status.Model),
		strings.TrimSpace(status.Agent),
		strings.TrimSpace(session.Controller.AgentName),
		strings.TrimSpace(session.Controller.Label),
		strings.TrimSpace(session.Controller.ControllerID),
	)
}

func acpControllerModeDisplay(status sdkcontroller.ControllerStatus) string {
	current := strings.TrimSpace(status.Mode)
	if current == "" {
		return ""
	}
	if mode, ok := matchACPControllerMode(status.ModeOptions, current); ok {
		return acpControllerModeLabel(mode)
	}
	return current
}

func nextACPControllerMode(status sdkcontroller.ControllerStatus) (sdkcontroller.ControllerMode, error) {
	modes := compactACPControllerModes(status.ModeOptions)
	if len(modes) == 0 {
		return sdkcontroller.ControllerMode{}, fmt.Errorf("tui/runtime: remote ACP controller did not declare session modes")
	}
	current := strings.TrimSpace(status.Mode)
	if current == "" {
		return modes[0], nil
	}
	for i, mode := range modes {
		if strings.EqualFold(strings.TrimSpace(mode.ID), current) || strings.EqualFold(strings.TrimSpace(mode.Name), current) {
			return modes[(i+1)%len(modes)], nil
		}
	}
	return modes[0], nil
}

func compactACPControllerModes(modes []sdkcontroller.ControllerMode) []sdkcontroller.ControllerMode {
	if len(modes) == 0 {
		return nil
	}
	out := make([]sdkcontroller.ControllerMode, 0, len(modes))
	seen := map[string]struct{}{}
	for _, mode := range modes {
		id := strings.TrimSpace(mode.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, sdkcontroller.ControllerMode{
			ID:          id,
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func matchACPControllerMode(modes []sdkcontroller.ControllerMode, requested string) (sdkcontroller.ControllerMode, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return sdkcontroller.ControllerMode{}, false
	}
	for _, mode := range modes {
		id := strings.TrimSpace(mode.ID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, requested) || strings.EqualFold(strings.TrimSpace(mode.Name), requested) {
			return mode, true
		}
	}
	return sdkcontroller.ControllerMode{}, false
}

func acpControllerModeLabel(mode sdkcontroller.ControllerMode) string {
	return firstNonEmpty(strings.TrimSpace(mode.Name), strings.TrimSpace(mode.ID))
}

func acpControllerEffortsForModel(status sdkcontroller.ControllerStatus, model string) []sdkcontroller.ControllerConfigChoice {
	model = strings.ToLower(strings.TrimSpace(model))
	if model != "" {
		for key, efforts := range status.EffortOptionsByModel {
			if strings.EqualFold(strings.TrimSpace(key), model) {
				return efforts
			}
		}
	}
	return status.EffortOptions
}

func controllerChoicesToSlashCandidates(choices []sdkcontroller.ControllerConfigChoice, detail string, query string, limit int) []SlashArgCandidate {
	if len(choices) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = len(choices)
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(choices)))
	for _, choice := range choices {
		value := strings.TrimSpace(choice.Value)
		if value == "" {
			continue
		}
		display := firstNonEmpty(strings.TrimSpace(choice.Name), value)
		candidateDetail := firstNonEmpty(strings.TrimSpace(choice.Description), detail)
		if query != "" && !hasSlashArgPrefix(query, value, display, candidateDetail) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   value,
			Display: display,
			Detail:  candidateDetail,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) completeModelAliases(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	ref := sdksession.SessionRef{}
	if session, ok := d.currentSession(); ok {
		ref = session.SessionRef
	}
	aliases, err := d.stack.ListModelAliases(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(aliases)))
	for _, alias := range aliases {
		display := strings.TrimSpace(alias)
		if display == "" {
			continue
		}
		if query != "" && !hasSlashArgPrefix(query, display) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   display,
			Display: display,
			Detail:  "configured model alias",
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) completeAgentCatalog(query string, limit int) []SlashArgCandidate {
	agents := d.agentCatalog(limit)
	if len(agents) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(agents)))
	for _, agent := range agents {
		if query != "" && !hasSlashArgPrefix(query, agent.Name, agent.Description) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   agent.Name,
			Display: agent.Name,
			Detail:  firstNonEmpty(agent.Description, "configured ACP agent"),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) completeBuiltInAgentCatalog(query string, limit int) []SlashArgCandidate {
	options := d.stack.ListBuiltinACPAgentAddOptions()
	if len(options) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(options)))
	for _, option := range options {
		if query != "" && !hasSlashArgPrefix(query, option.Value, option.Display, option.Detail) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   option.Value,
			Display: option.Display,
			Detail:  firstNonEmpty(option.Detail, "built-in ACP agent"),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) completeInstallableBuiltInAgentCatalog(query string, limit int) []SlashArgCandidate {
	options := d.stack.ListInstallableACPAgentOptions()
	if len(options) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(options)))
	for _, option := range options {
		if query != "" && !hasSlashArgPrefix(query, option.Value, option.Display, option.Detail) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   option.Value,
			Display: option.Display,
			Detail:  firstNonEmpty(option.Detail, "install ACP agent adapter"),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) completeAgentParticipants(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	session, ok := d.currentSession()
	if !ok {
		return nil, nil
	}
	state, err := d.stack.Gateway.ControlPlaneState(ctx, appgateway.ControlPlaneStateRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(state.Participants)))
	for _, participant := range state.Participants {
		id := strings.TrimSpace(participant.ID)
		label := strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID))
		if id == "" {
			continue
		}
		if query != "" && !hasSlashArgPrefix(query, id, label, participant.SessionID, string(participant.Role)) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   id,
			Display: label,
			Detail:  strings.Join(compactNonEmpty([]string{string(participant.Role), strings.TrimSpace(participant.SessionID)}), " · "),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *GatewayDriver) completeAgentHandoffTargets(ctx context.Context, query string, limit int) ([]SlashArgCandidate, error) {
	out := []SlashArgCandidate{{
		Value:   "local",
		Display: "local",
		Detail:  "return to local kernel",
	}}
	if query != "" && !hasSlashArgPrefix(query, "local", "kernel") {
		out = nil
	}
	for _, agent := range d.completeAgentCatalog(query, limit) {
		out = append(out, SlashArgCandidate{
			Value:   agent.Value,
			Display: agent.Display,
			Detail:  agent.Detail,
		})
		if len(out) >= limit {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (d *GatewayDriver) agentCatalog(limit int) []AgentCandidate {
	available := d.stack.ListACPAgents()
	if len(available) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = len(available)
	}
	out := make([]AgentCandidate, 0, min(limit, len(available)))
	for _, agent := range available {
		out = append(out, AgentCandidate{
			Name:        strings.TrimSpace(agent.Name),
			Description: strings.TrimSpace(agent.Description),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (d *GatewayDriver) resolveHandoffAgentName(ctx context.Context, ref sdksession.SessionRef, input string) (string, error) {
	if agent, err := d.resolveAgentName(input); err == nil {
		return agent, nil
	}
	participantID, err := d.resolveParticipantID(ctx, ref, input)
	if err != nil {
		return "", err
	}
	state, err := d.stack.Gateway.ControlPlaneState(ctx, appgateway.ControlPlaneStateRequest{SessionRef: ref})
	if err != nil {
		return "", err
	}
	for _, participant := range state.Participants {
		if strings.EqualFold(strings.TrimSpace(participant.ID), participantID) {
			return strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID)), nil
		}
	}
	return "", fmt.Errorf("tui/runtime: participant %q is not attached", input)
}

func (d *GatewayDriver) resolveAgentName(input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("tui/runtime: agent name is required")
	}
	var exact string
	prefixMatches := make([]string, 0, 2)
	for _, agent := range d.agentCatalog(0) {
		name := strings.TrimSpace(agent.Name)
		normalized := strings.ToLower(name)
		if normalized == "" {
			continue
		}
		if normalized == input {
			exact = name
			break
		}
		if strings.HasPrefix(normalized, input) {
			prefixMatches = append(prefixMatches, name)
		}
	}
	if exact != "" {
		return exact, nil
	}
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0], nil
	case 0:
		return "", fmt.Errorf("tui/runtime: agent %q is not configured", input)
	default:
		return "", fmt.Errorf("tui/runtime: agent %q is ambiguous", input)
	}
}

func (d *GatewayDriver) resolveParticipantID(ctx context.Context, ref sdksession.SessionRef, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("tui/runtime: participant id is required")
	}
	state, err := d.stack.Gateway.ControlPlaneState(ctx, appgateway.ControlPlaneStateRequest{SessionRef: ref})
	if err != nil {
		return "", err
	}
	var exact string
	prefixMatches := make([]string, 0, 2)
	for _, participant := range state.Participants {
		if participant.Kind != sdksession.ParticipantKindACP {
			continue
		}
		id := strings.TrimSpace(participant.ID)
		label := strings.TrimSpace(participant.Label)
		handle := strings.TrimPrefix(label, "@")
		sessionID := strings.TrimSpace(participant.SessionID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, input) || strings.EqualFold(label, input) || strings.EqualFold(handle, input) || strings.EqualFold(sessionID, input) {
			return id, nil
		}
		for _, candidate := range []string{id, label, handle, sessionID} {
			candidate = strings.ToLower(strings.TrimSpace(candidate))
			if candidate != "" && strings.HasPrefix(candidate, input) {
				exact = id
				prefixMatches = append(prefixMatches, exact)
				break
			}
		}
	}
	switch len(dedupeNonEmptyStrings(prefixMatches)) {
	case 1:
		return dedupeNonEmptyStrings(prefixMatches)[0], nil
	case 0:
		return "", fmt.Errorf("tui/runtime: participant %q is not attached", input)
	default:
		return "", fmt.Errorf("tui/runtime: participant %q is ambiguous", input)
	}
}

func (d *GatewayDriver) resolveStoredModelAlias(ctx context.Context, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("tui/runtime: model alias is required")
	}
	ref := sdksession.SessionRef{}
	if session, ok := d.currentSession(); ok {
		ref = session.SessionRef
	}
	aliases, err := d.stack.ListModelAliases(ctx, ref)
	if err != nil {
		return "", err
	}
	var exact string
	prefixMatches := make([]string, 0, 2)
	for _, alias := range aliases {
		normalized := strings.ToLower(strings.TrimSpace(alias))
		if normalized == "" {
			continue
		}
		if normalized == input {
			exact = strings.TrimSpace(alias)
			break
		}
		if strings.HasPrefix(normalized, input) {
			prefixMatches = append(prefixMatches, strings.TrimSpace(alias))
		}
	}
	if exact != "" {
		return exact, nil
	}
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0], nil
	case 0:
		return "", fmt.Errorf("tui/runtime: unknown model alias %q", input)
	default:
		return "", fmt.Errorf("tui/runtime: ambiguous model alias %q", input)
	}
}

func hasSlashArgPrefix(query string, values ...string) bool {
	if query == "" {
		return true
	}
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, query) {
			return true
		}
	}
	return false
}

func validateConnectConfig(tpl providerTemplate, cfg ConnectConfig) error {
	if strings.TrimSpace(cfg.Model) == "" {
		return fmt.Errorf("model is required; use /connect and choose or type a model name")
	}
	if baseURL := strings.TrimSpace(cfg.BaseURL); baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("base URL is invalid; use a full URL such as %s", tpl.defaultBaseURL)
		}
	}
	if tpl.noAuthRequired {
		return nil
	}
	if strings.TrimSpace(cfg.APIKey) != "" || strings.TrimSpace(cfg.TokenEnv) != "" {
		return nil
	}
	envHint := defaultTokenEnvName(tpl.provider)
	if envHint == "" {
		envHint = "YOUR_API_KEY"
	}
	return fmt.Errorf("API key is missing; paste a key or enter env:%s in /connect", envHint)
}

func parseTokenEnvSpec(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(strings.ToLower(trimmed), "env:"):
		env := strings.TrimSpace(trimmed[len("env:"):])
		return env, env != ""
	case strings.HasPrefix(trimmed, "$"):
		env := strings.TrimSpace(strings.TrimPrefix(trimmed, "$"))
		return env, env != ""
	default:
		return "", false
	}
}

func defaultTokenEnvName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "minimax":
		return "MINIMAX_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "openai-compatible":
		return "OPENAI_COMPATIBLE_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "anthropic-compatible":
		return "ANTHROPIC_COMPATIBLE_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "xiaomi":
		return "XIAOMI_API_KEY"
	case "volcengine":
		return "VOLCENGINE_API_KEY"
	default:
		return ""
	}
}

type gatewayTurn struct {
	handle appgateway.TurnHandle
}

func (t gatewayTurn) HandleID() string                  { return t.handle.HandleID() }
func (t gatewayTurn) RunID() string                     { return t.handle.RunID() }
func (t gatewayTurn) TurnID() string                    { return t.handle.TurnID() }
func (t gatewayTurn) SessionRef() sdksession.SessionRef { return t.handle.SessionRef() }
func (t gatewayTurn) Events() <-chan appgateway.EventEnvelope {
	return t.handle.Events()
}
func (t gatewayTurn) Submit(ctx context.Context, req appgateway.SubmitRequest) error {
	return t.handle.Submit(ctx, req)
}
func (t gatewayTurn) Cancel() bool { return t.handle.Cancel() }
func (t gatewayTurn) Close() error { return t.handle.Close() }

func defaultSlashArgCandidates(command string) []SlashArgCandidate {
	switch command {
	case "agent":
		return []SlashArgCandidate{
			{Value: "use", Display: "use", Detail: "Switch the main controller"},
			{Value: "add", Display: "add", Detail: "Register a built-in ACP agent"},
			{Value: "install", Display: "install", Detail: "Install and register an external ACP adapter"},
			{Value: "list", Display: "list", Detail: "List registered ACP agents"},
			{Value: "remove", Display: "remove", Detail: "Unregister an ACP agent"},
		}
	case "sandbox":
		return sandboxCandidates()
	case "model":
		return []SlashArgCandidate{
			{Value: "use", Display: "use", Detail: "Switch current model alias"},
			{Value: "del", Display: "del", Detail: "Delete stored model alias"},
		}
	default:
		return nil
	}
}

func sandboxCandidates() []SlashArgCandidate {
	switch runtime.GOOS {
	case "darwin":
		return []SlashArgCandidate{
			{Value: "auto", Display: "auto", Detail: "Use the default macOS sandbox backend"},
			{Value: "seatbelt", Display: "seatbelt", Detail: "Use sandbox-exec seatbelt isolation"},
		}
	case "linux":
		return []SlashArgCandidate{
			{Value: "auto", Display: "auto", Detail: "Prefer bwrap, then fall back to landlock"},
			{Value: "bwrap", Display: "bwrap", Detail: "Use bubblewrap container isolation"},
			{Value: "landlock", Display: "landlock", Detail: "Use the landlock helper sandbox"},
		}
	default:
		return []SlashArgCandidate{{Value: "auto", Display: "auto", Detail: "Use the default sandbox backend"}}
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

func (d *GatewayDriver) defaultDisplays() (string, string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.defaultModelText, d.defaultSessionMode, d.defaultSandboxType
}

func (d *GatewayDriver) refreshSessionDisplay(ctx context.Context, session sdksession.Session) {
	if d == nil || d.stack == nil {
		return
	}
	modelText, sessionMode, sandboxType := d.defaultDisplays()
	if alias := strings.TrimSpace(d.stack.DefaultModelAlias()); alias != "" {
		modelText = alias
	}
	if state, err := d.stack.SessionRuntimeState(ctx, session.SessionRef); err == nil {
		if strings.TrimSpace(state.ModelAlias) != "" {
			modelText = strings.TrimSpace(state.ModelAlias)
		}
		if strings.TrimSpace(state.SessionMode) != "" {
			sessionMode = strings.TrimSpace(state.SessionMode)
		}
	}
	sandbox := d.stack.SandboxStatus()
	sandboxType = firstNonEmpty(sandbox.ResolvedBackend, sandbox.RequestedBackend, sandboxType)
	d.mu.Lock()
	d.modelText = modelText
	d.sessionMode = sessionMode
	d.sandboxType = sandboxType
	d.mu.Unlock()
}

func authTypeFromString(s string) sdkproviders.AuthType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "api_key", "apikey":
		return sdkproviders.AuthAPIKey
	case "bearer_token", "bearer":
		return sdkproviders.AuthBearerToken
	case "oauth_token", "oauth":
		return sdkproviders.AuthOAuthToken
	case "none":
		return sdkproviders.AuthNone
	default:
		return sdkproviders.AuthAPIKey
	}
}
