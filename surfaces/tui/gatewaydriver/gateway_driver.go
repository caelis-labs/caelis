package gatewaydriver

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

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
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
	streamParents       map[string]terminalStreamParent
}

type terminalStreamParent struct {
	CallID   string
	ToolName string
	RawInput map[string]any
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
		defaultSandboxType:  firstNonEmpty(stack.SandboxStatus().ResolvedBackend, stack.SandboxStatus().RequestedBackend, "auto"),
		sandboxType:         firstNonEmpty(stack.SandboxStatus().ResolvedBackend, stack.SandboxStatus().RequestedBackend, "auto"),
		streamSubscriptions: map[string]struct{}{},
		streamParents:       map[string]terminalStreamParent{},
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
	d.bindTerminalStreamRequest(&req)
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

func (d *GatewayDriver) bindTerminalStreamRequest(req *kernel.StreamRequest) {
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

func (d *GatewayDriver) lookupTerminalStreamParentLocked(req kernel.StreamRequest) (terminalStreamParent, bool) {
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

func terminalStreamParentKeys(req kernel.StreamRequest) []string {
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
		FullAccessMode:          false,
		Surface:                 bindingKey,
	}
	if d.stack != nil {
		req := DoctorRequest{}
		if ok {
			req.SessionRef = activeSession.SessionRef
		}
		if report, err := d.stack.Doctor(context.Background(), req); err == nil {
			status.StoreDir = strings.TrimSpace(report.StoreDir)
			status.Provider = strings.TrimSpace(report.ActiveProvider)
			status.ModelName = strings.TrimSpace(report.ActiveModel)
			status.MissingAPIKey = report.MissingAPIKey
			status.HostExecution = report.HostExecution
			status.FullAccessMode = report.FullAccessMode
			status.PermissionGrantCount = report.PermissionGrantCount
			status.PermissionGrantNetwork = report.PermissionGrantNetwork
			status.PermissionReadRootCount = report.PermissionReadRootCount
			status.PermissionWriteRootCount = report.PermissionWriteRootCount
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
			if usage, err := d.stack.SessionUsageSnapshot(context.Background(), activeSession.SessionRef, rawModelText); err == nil {
				status.TotalTokens = usage.TotalTokens
				status.ContextWindowTokens = usage.ContextWindowTokens
			}
		}
		if ok {
			if usage, err := d.sessionTokenUsageBreakdown(context.Background(), activeSession.SessionRef); err == nil {
				status.SessionUsageTotal = usage.Total
				status.SessionUsageMain = usage.Main
				status.SessionUsageSubagents = usage.Subagents
				status.SessionUsageAutoReview = usage.AutoReview
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
		if strings.TrimSpace(status.SessionMode) == "" {
			status.SessionMode = acpModeID
		}
		if strings.TrimSpace(status.ModeLabel) == "" {
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

func (d *GatewayDriver) sessionTokenUsage(ctx context.Context, ref session.SessionRef) (kernel.UsageSnapshot, error) {
	breakdown, err := d.sessionTokenUsageBreakdown(ctx, ref)
	if err != nil {
		return kernel.UsageSnapshot{}, err
	}
	return breakdown.Total, nil
}

type sessionTokenUsageBreakdown struct {
	Total      kernel.UsageSnapshot
	Main       kernel.UsageSnapshot
	Subagents  kernel.UsageSnapshot
	AutoReview kernel.UsageSnapshot
}

const (
	tokenUsageCategoryMain       = "main"
	tokenUsageCategorySubagent   = "subagent"
	tokenUsageCategoryAutoReview = "auto_review"
)

func (d *GatewayDriver) sessionTokenUsageBreakdown(ctx context.Context, ref session.SessionRef) (sessionTokenUsageBreakdown, error) {
	if d == nil || d.stack == nil || d.stack.Sessions == nil {
		return sessionTokenUsageBreakdown{}, nil
	}
	if strings.TrimSpace(ref.SessionID) == "" {
		return sessionTokenUsageBreakdown{}, nil
	}
	events, err := d.stack.Sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return sessionTokenUsageBreakdown{}, err
	}
	breakdown := sessionTokenUsageBreakdownFromEvents(events, tokenUsageCategoryMain)
	if state, err := d.stack.Sessions.SnapshotState(ctx, ref); err == nil {
		breakdown.addBreakdown(sessionTokenUsageBreakdownFromState(state))
	}
	for _, childRef := range d.selfSubagentSessionRefs(ctx, ref) {
		childEvents, err := d.stack.Sessions.Events(ctx, session.EventsRequest{SessionRef: childRef})
		if err != nil {
			continue
		}
		childBreakdown := sessionTokenUsageBreakdownFromEvents(childEvents, tokenUsageCategorySubagent)
		if state, err := d.stack.Sessions.SnapshotState(ctx, childRef); err == nil {
			childBreakdown.addBreakdown(sessionTokenUsageBreakdownFromState(state))
		}
		breakdown.addBreakdown(childBreakdown)
	}
	return breakdown, nil
}

func sessionTokenUsageBreakdownFromEvents(events []*session.Event, fallbackCategory string) sessionTokenUsageBreakdown {
	var breakdown sessionTokenUsageBreakdown
	lastToolCallUsageKey := ""
	lastUsageWasToolCall := false
	for _, event := range events {
		one := kernel.UsageSnapshotFromSessionEvent(event)
		if one == nil {
			if session.EventTypeOf(event) != session.EventTypeToolCall {
				lastToolCallUsageKey = ""
				lastUsageWasToolCall = false
			}
			continue
		}
		isToolCall := session.EventTypeOf(event) == session.EventTypeToolCall
		usageKey := usageSnapshotDedupeKey(*one)
		if isToolCall && lastUsageWasToolCall && usageKey != "" && usageKey == lastToolCallUsageKey {
			continue
		}
		breakdown.add(usageCategoryFromSessionEvent(event, fallbackCategory), *one)
		if isToolCall {
			lastToolCallUsageKey = usageKey
			lastUsageWasToolCall = true
		} else {
			lastToolCallUsageKey = ""
			lastUsageWasToolCall = false
		}
	}
	return breakdown
}

func sessionTokenUsageBreakdownFromState(state map[string]any) sessionTokenUsageBreakdown {
	var breakdown sessionTokenUsageBreakdown
	accounting := mapAnyValue(state[kernel.StateUsageAccounting])
	if usage := kernel.UsageSnapshotFromMap(mapAnyValue(accounting[tokenUsageCategoryAutoReview])); usage != nil {
		breakdown.add(tokenUsageCategoryAutoReview, *usage)
	}
	return breakdown
}

func (u *sessionTokenUsageBreakdown) add(category string, usage kernel.UsageSnapshot) {
	if u == nil {
		return
	}
	addUsageSnapshot(&u.Total, usage)
	switch strings.TrimSpace(category) {
	case tokenUsageCategoryAutoReview:
		addUsageSnapshot(&u.AutoReview, usage)
	case tokenUsageCategorySubagent:
		addUsageSnapshot(&u.Subagents, usage)
	default:
		addUsageSnapshot(&u.Main, usage)
	}
}

func (u *sessionTokenUsageBreakdown) addBreakdown(other sessionTokenUsageBreakdown) {
	if u == nil {
		return
	}
	addUsageSnapshot(&u.Total, other.Total)
	addUsageSnapshot(&u.Main, other.Main)
	addUsageSnapshot(&u.Subagents, other.Subagents)
	addUsageSnapshot(&u.AutoReview, other.AutoReview)
}

func addUsageSnapshot(total *kernel.UsageSnapshot, usage kernel.UsageSnapshot) {
	if total == nil {
		return
	}
	total.PromptTokens += usage.PromptTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.CompletionTokens += usage.CompletionTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
}

func usageCategoryFromSessionEvent(event *session.Event, fallback string) string {
	if event == nil {
		return firstNonEmpty(fallback, tokenUsageCategoryMain)
	}
	if category := usageCategoryFromMeta(event.Meta); category != "" {
		return category
	}
	if event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantKindSubagent {
		return tokenUsageCategorySubagent
	}
	return firstNonEmpty(fallback, tokenUsageCategoryMain)
}

func usageCategoryFromMeta(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	for _, key := range []string{"usage_category", "usageCategory", "category"} {
		if category := normalizeUsageCategory(anyString(meta[key])); category != "" {
			return category
		}
	}
	if category := normalizeUsageCategory(anyString(nestedMapAny(meta, "caelis", "usage", "category"))); category != "" {
		return category
	}
	if category := normalizeUsageCategory(anyString(nestedMapAny(meta, "caelis", "sdk", "usage_category"))); category != "" {
		return category
	}
	if strings.EqualFold(anyString(meta["decision_source"]), "auto-review") ||
		strings.EqualFold(anyString(meta["source"]), "auto_review") {
		return tokenUsageCategoryAutoReview
	}
	return ""
}

func nestedMapAny(values map[string]any, path ...string) any {
	if len(values) == 0 {
		return nil
	}
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}

func mapAnyValue(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return maps.Clone(typed)
	}
	return nil
}

func normalizeUsageCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(category, "-", "_"))) {
	case "auto_review", "autoreview", "review":
		return tokenUsageCategoryAutoReview
	case "subagent", "sub_agent", "child", "child_agent":
		return tokenUsageCategorySubagent
	case "main", "controller":
		return tokenUsageCategoryMain
	default:
		return ""
	}
}

func (d *GatewayDriver) selfSubagentSessionRefs(ctx context.Context, ref session.SessionRef) []session.SessionRef {
	if d == nil || d.stack == nil || d.stack.Sessions == nil {
		return nil
	}
	activeSession, err := d.stack.Sessions.Session(ctx, ref)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]session.SessionRef, 0, len(activeSession.Participants))
	for _, participant := range activeSession.Participants {
		if participant.Kind != session.ParticipantKindSubagent {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(participant.AgentName), "self") {
			continue
		}
		sessionID := strings.TrimSpace(participant.SessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		childRef := ref
		childRef.SessionID = sessionID
		out = append(out, session.NormalizeSessionRef(childRef))
	}
	return out
}

func usageSnapshotDedupeKey(usage kernel.UsageSnapshot) string {
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.ReasoningTokens == 0 && usage.TotalTokens == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d/%d/%d/%d", usage.PromptTokens, usage.CachedInputTokens, usage.CompletionTokens, usage.ReasoningTokens, usage.TotalTokens)
}

func (d *GatewayDriver) Submit(ctx context.Context, submission Submission) (Turn, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	if isBuiltInControllerSession(activeSession) && activeKernelTurnForSession(gw.ActiveTurns(), activeSession.SessionRef) {
		err := gw.SubmitActiveTurn(ctx, kernel.SubmitActiveTurnRequest{
			SessionRef: activeSession.SessionRef,
			Kind:       kernel.SubmissionKindConversation,
			Text:       strings.TrimSpace(submission.Text),
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
		SessionRef: activeSession.SessionRef,
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
	if cfg.BaseURL == "" {
		cfg.BaseURL = tpl.defaultBaseURL
	}
	endpoint, hasEndpoint := connectEndpointForBaseURL(tpl, cfg.BaseURL)
	if strings.TrimSpace(cfg.EndpointID) == "" && hasEndpoint {
		cfg.EndpointID = endpoint.id
	}
	if err := validateConnectConfig(tpl, cfg); err != nil {
		if !d.hasReusableConnectAuth(ctx, tpl.provider, cfg.BaseURL) {
			return StatusSnapshot{}, err
		}
	}
	if defaults, err := connectDefaultsForConfigWithStack(ctx, d.stack, cfg); err == nil {
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
	api := tpl.api
	if hasEndpoint && strings.TrimSpace(string(endpoint.api)) != "" {
		api = endpoint.api
	}
	if tpl.provider == "codefree" {
		if err := d.stack.EnsureCodeFreeAuth(ctx, CodeFreeAuthRequest{
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
	authType := defaultConnectAuthType(tpl.provider)
	if strings.TrimSpace(cfg.AuthType) != "" {
		authType = authTypeFromString(strings.TrimSpace(cfg.AuthType))
	}
	if tpl.noAuthRequired {
		authType = model.AuthNone
	}
	persistToken := strings.TrimSpace(cfg.APIKey) != "" && strings.TrimSpace(cfg.TokenEnv) == ""
	reasoningLevels := normalizeReasoningLevels(cfg.ReasoningLevels)
	defaultReasoningEffort := strings.TrimSpace(cfg.ReasoningEffort)
	alias, err := d.stack.Connect(ModelConfig{
		Provider:               strings.TrimSpace(cfg.Provider),
		EndpointID:             strings.TrimSpace(cfg.EndpointID),
		API:                    api,
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
	if activeSession, ok := d.currentSession(); ok && alias != "" {
		if err := d.stack.UseModel(ctx, activeSession.SessionRef, alias); err != nil {
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

func (d *GatewayDriver) hasReusableConnectAuth(ctx context.Context, provider string, baseURL string) bool {
	if d == nil || d.stack == nil {
		return false
	}
	normalizedBaseURL := normalizedConnectBaseURL(baseURL)
	if normalizedBaseURL == "" {
		return false
	}
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := d.stack.ListModelChoices(ctx, ref)
	if err != nil {
		return false
	}
	for _, choice := range choices {
		if !strings.EqualFold(strings.TrimSpace(choice.Provider), strings.TrimSpace(provider)) {
			continue
		}
		if normalizedConnectBaseURL(choice.BaseURL) == normalizedBaseURL {
			return true
		}
	}
	return false
}

func (d *GatewayDriver) UseModel(ctx context.Context, model string, reasoningEffort ...string) (StatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
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
		status, err := d.stack.SetACPControllerModel(ctx, activeSession.SessionRef, strings.TrimSpace(model), reasoning)
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
		return StatusSnapshot{}, fmt.Errorf("surfaces/tui/gatewaydriver: model alias is required")
	}
	reasoning := ""
	if len(reasoningEffort) > 0 {
		reasoning = strings.TrimSpace(reasoningEffort[0])
		if reasoning != "" && !d.modelAliasSupportsReasoningLevel(alias, reasoning) {
			return StatusSnapshot{}, fmt.Errorf("surfaces/tui/gatewaydriver: model %q does not support reasoning level %q", alias, reasoning)
		}
	}
	if err := d.stack.UseModel(ctx, activeSession.SessionRef, alias, reasoning); err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.modelText = alias
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) DeleteModel(ctx context.Context, alias string) error {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return err
	}
	resolved, err := d.resolveStoredModelAlias(ctx, strings.TrimSpace(alias))
	if err != nil {
		return err
	}
	if err := d.stack.DeleteModel(ctx, activeSession.SessionRef, resolved); err != nil {
		return err
	}
	d.mu.Lock()
	d.defaultModelText = strings.TrimSpace(d.stack.DefaultModelAlias())
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, activeSession)
	return nil
}

func (d *GatewayDriver) CycleSessionMode(ctx context.Context) (StatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	normalized, err := d.stack.CycleSessionMode(ctx, activeSession.SessionRef)
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

func (d *GatewayDriver) SetSessionMode(ctx context.Context, mode string) (StatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	normalized, err := d.stack.SetSessionMode(ctx, activeSession.SessionRef, mode)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sessionMode = normalized
	d.mu.Unlock()
	status, err := d.Status(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	status.SessionMode = normalized
	status.ModeLabel = normalized
	return status, nil
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

func (d *GatewayDriver) AddAgent(ctx context.Context, target string) (AgentStatusSnapshot, error) {
	return d.AddAgentWithOptions(ctx, target, AgentAddOptions{})
}

func (d *GatewayDriver) AddAgentWithOptions(ctx context.Context, target string, opts AgentAddOptions) (AgentStatusSnapshot, error) {
	if opts.Custom != nil {
		if err := d.stack.RegisterACPAgent(ctx, *opts.Custom); err != nil {
			return AgentStatusSnapshot{}, err
		}
		return d.AgentStatus(ctx)
	}
	if opts.Install {
		var finish func()
		ctx, finish = d.beginInterruptibleCommand(ctx)
		defer finish()
	}
	if err := d.stack.RegisterBuiltinACPAgentWithOptions(ctx, target, RegisterBuiltinACPAgentOptions{Install: opts.Install}); err != nil {
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
	if strings.EqualFold(strings.TrimSpace(status.ControllerKind), string(session.ControllerKindACP)) {
		return AgentStatusSnapshot{}, fmt.Errorf("surfaces/tui/gatewaydriver: an ACP agent is the active controller; run /agent use local before removing registered agents")
	}
	if err := d.stack.UnregisterACPAgent(target); err != nil {
		return AgentStatusSnapshot{}, err
	}
	return d.AgentStatus(ctx)
}

func (d *GatewayDriver) HandoffAgent(ctx context.Context, target string) (AgentStatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	target = strings.TrimSpace(target)
	req := kernel.HandoffControllerRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: d.bindingKey,
		Source:     "tui_agent_handoff",
	}
	switch strings.ToLower(target) {
	case "", "main", "local", "kernel":
		req.Kind = session.ControllerKindKernel
		req.Reason = "resume local control"
	default:
		agent, resolveErr := d.resolveAgentName(target)
		if resolveErr != nil {
			return AgentStatusSnapshot{}, resolveErr
		}
		req.Kind = session.ControllerKindACP
		req.Agent = agent
		req.Reason = "handoff to agent"
	}
	gw, err := d.gateway()
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	updated, err := gw.HandoffController(ctx, req)
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
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	agent, err := d.resolveAgentName(target)
	if err != nil {
		return nil, err
	}
	label := d.allocateSideAgentLabel(ctx, activeSession.SessionRef, agent)
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	updated, err := gw.AttachParticipant(ctx, kernel.AttachParticipantRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: d.bindingKey,
		Agent:      agent,
		Role:       session.ParticipantRoleSidecar,
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
	result, err := gw.PromptParticipant(ctx, kernel.PromptParticipantRequest{
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

func (d *GatewayDriver) detachSideAgentAfterPromptFailure(ctx context.Context, ref session.SessionRef, participantID string) error {
	participantID = strings.TrimSpace(participantID)
	if participantID == "" || d == nil || d.stack == nil {
		return nil
	}
	gw, err := d.gateway()
	if err != nil {
		return nil
	}
	updated, err := gw.DetachParticipant(context.WithoutCancel(ctx), kernel.DetachParticipantRequest{
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

func (d *GatewayDriver) allocateSideAgentLabel(ctx context.Context, ref session.SessionRef, agent string) string {
	used := map[string]struct{}{}
	if gw, err := d.gateway(); err == nil {
		if state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: ref}); err == nil {
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
	base := normalizeAgentHandleBase(agent)
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

func normalizeAgentHandleBase(value string) string {
	value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		var keep rune
		switch {
		case r >= 'a' && r <= 'z':
			keep = r
		case r >= '0' && r <= '9':
			keep = r
		case r == '-' || r == '_':
			keep = r
		case r == '/' || r == '.' || r == ' ' || r == '\t':
			if !lastDash && b.Len() > 0 {
				keep = '-'
				lastDash = true
			}
		}
		if keep == 0 {
			continue
		}
		if keep != '-' {
			lastDash = false
		}
		b.WriteRune(keep)
	}
	return strings.Trim(b.String(), "-_")
}

func sideAgentParticipantID(activeSession session.Session, agent string, label string) (string, error) {
	agent = strings.TrimSpace(agent)
	label = strings.TrimSpace(label)
	for i := len(activeSession.Participants) - 1; i >= 0; i-- {
		participant := activeSession.Participants[i]
		if participant.Role != session.ParticipantRoleSidecar {
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
	return "", fmt.Errorf("surfaces/tui/gatewaydriver: side ACP participant %q was not attached", agent)
}

func (d *GatewayDriver) ContinueSubagent(ctx context.Context, handle string, prompt string) (Turn, error) {
	activeSession, err := d.ensureSession(ctx)
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
	activeSession, ok := d.currentSession()
	if !ok {
		return []CompletionCandidate{}, nil
	}
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: activeSession.SessionRef})
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

func isUserSideParticipant(participant kernel.ParticipantState) bool {
	if participant.Role != session.ParticipantRoleSidecar {
		return false
	}
	return participant.Kind == session.ParticipantKindACP
}

func (d *GatewayDriver) CompleteFile(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	return completeWorkspaceFiles(ctx, d.WorkspaceDir(), query, limit)
}

func (d *GatewayDriver) CompleteSkill(ctx context.Context, query string, limit int) ([]CompletionCandidate, error) {
	limit = normalizeCompletionLimit(limit)

	skills, err := d.stack.DiscoverSkills(ctx, d.WorkspaceDir())
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

func (d *GatewayDriver) completeACPControllerSlashArg(status controller.ControllerStatus, command string, query string, limit int) ([]SlashArgCandidate, bool) {
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
	levels := d.configuredModelReasoningLevels(cfg)
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
	for _, one := range d.configuredModelReasoningLevels(cfg) {
		if strings.EqualFold(strings.TrimSpace(one), strings.TrimSpace(level)) {
			return true
		}
	}
	return false
}

func (d *GatewayDriver) configuredModelReasoningLevels(cfg ModelConfig) []string {
	levels := normalizeReasoningLevels(cfg.ReasoningLevels)
	for _, level := range normalizeReasoningLevels(reasoningLevelsForModel(stackForDriver(d), cfg.Provider, cfg.Model)) {
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

func controllerCommandNames(commands []controller.ControllerCommand) []string {
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

func acpControllerModelText(status controller.ControllerStatus, activeSession session.Session) string {
	return firstNonEmpty(
		strings.TrimSpace(status.Model),
		strings.TrimSpace(status.Agent),
		strings.TrimSpace(activeSession.Controller.AgentName),
		strings.TrimSpace(activeSession.Controller.Label),
		strings.TrimSpace(activeSession.Controller.ControllerID),
	)
}

func acpControllerModeDisplay(status controller.ControllerStatus) string {
	current := strings.TrimSpace(status.Mode)
	if current == "" {
		return ""
	}
	if mode, ok := matchACPControllerMode(status.ModeOptions, current); ok {
		return acpControllerModeLabel(mode)
	}
	return current
}

func nextACPControllerMode(status controller.ControllerStatus) (controller.ControllerMode, error) {
	modes := compactACPControllerModes(status.ModeOptions)
	if len(modes) == 0 {
		return controller.ControllerMode{}, fmt.Errorf("surfaces/tui/gatewaydriver: remote ACP controller did not declare session modes")
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

func compactACPControllerModes(modes []controller.ControllerMode) []controller.ControllerMode {
	if len(modes) == 0 {
		return nil
	}
	out := make([]controller.ControllerMode, 0, len(modes))
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
		out = append(out, controller.ControllerMode{
			ID:          id,
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func matchACPControllerMode(modes []controller.ControllerMode, requested string) (controller.ControllerMode, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return controller.ControllerMode{}, false
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
	return controller.ControllerMode{}, false
}

func acpControllerModeLabel(mode controller.ControllerMode) string {
	return firstNonEmpty(strings.TrimSpace(mode.Name), strings.TrimSpace(mode.ID))
}

func acpControllerEffortsForModel(status controller.ControllerStatus, model string) []controller.ControllerConfigChoice {
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

func controllerChoicesToSlashCandidates(choices []controller.ControllerConfigChoice, detail string, query string, limit int) []SlashArgCandidate {
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
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := d.stack.ListModelChoices(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]SlashArgCandidate, 0, min(limit, len(choices)))
	for _, choice := range choices {
		value := strings.TrimSpace(firstNonEmpty(choice.ID, choice.Alias))
		display := strings.TrimSpace(firstNonEmpty(choice.Alias, choice.ID))
		if display == "" {
			continue
		}
		if query != "" && !hasSlashArgPrefix(query, display) && !hasSlashArgPrefix(query, value) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   value,
			Display: display,
			Detail:  firstNonEmpty(strings.TrimSpace(choice.Detail), "configured model alias"),
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
	activeSession, ok := d.currentSession()
	if !ok {
		return nil, nil
	}
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{
		SessionRef: activeSession.SessionRef,
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

func (d *GatewayDriver) resolveHandoffAgentName(ctx context.Context, ref session.SessionRef, input string) (string, error) {
	if agent, err := d.resolveAgentName(input); err == nil {
		return agent, nil
	}
	participantID, err := d.resolveParticipantID(ctx, ref, input)
	if err != nil {
		return "", err
	}
	gw, err := d.gateway()
	if err != nil {
		return "", err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: ref})
	if err != nil {
		return "", err
	}
	for _, participant := range state.Participants {
		if strings.EqualFold(strings.TrimSpace(participant.ID), participantID) {
			return strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID)), nil
		}
	}
	return "", fmt.Errorf("surfaces/tui/gatewaydriver: participant %q is not attached", input)
}

func (d *GatewayDriver) resolveAgentName(input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: agent name is required")
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
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: agent %q is not configured", input)
	default:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: agent %q is ambiguous", input)
	}
}

func (d *GatewayDriver) resolveParticipantID(ctx context.Context, ref session.SessionRef, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: participant id is required")
	}
	gw, err := d.gateway()
	if err != nil {
		return "", err
	}
	state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: ref})
	if err != nil {
		return "", err
	}
	var exact string
	prefixMatches := make([]string, 0, 2)
	for _, participant := range state.Participants {
		if participant.Kind != session.ParticipantKindACP {
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
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: participant %q is not attached", input)
	default:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: participant %q is ambiguous", input)
	}
}

func (d *GatewayDriver) resolveStoredModelAlias(ctx context.Context, input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: model alias is required")
	}
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := d.stack.ListModelChoices(ctx, ref)
	if err != nil {
		return "", err
	}
	var exact string
	exactAliasMatches := make([]string, 0, 2)
	prefixMatches := make([]string, 0, 2)
	for _, choice := range choices {
		id := strings.TrimSpace(firstNonEmpty(choice.ID, choice.Alias))
		alias := strings.TrimSpace(choice.Alias)
		normalizedID := strings.ToLower(id)
		normalizedAlias := strings.ToLower(alias)
		if normalizedID == "" && normalizedAlias == "" {
			continue
		}
		if normalizedID == input {
			exact = id
			break
		}
		if normalizedAlias == input {
			exactAliasMatches = append(exactAliasMatches, id)
			continue
		}
		if strings.HasPrefix(normalizedID, input) || strings.HasPrefix(normalizedAlias, input) {
			prefixMatches = append(prefixMatches, id)
		}
	}
	if exact != "" {
		return exact, nil
	}
	switch len(dedupeNonEmptyStrings(exactAliasMatches)) {
	case 1:
		return dedupeNonEmptyStrings(exactAliasMatches)[0], nil
	case 0:
	default:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: ambiguous model alias %q", input)
	}
	prefixMatches = dedupeNonEmptyStrings(prefixMatches)
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0], nil
	case 0:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: unknown model alias %q", input)
	default:
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: ambiguous model alias %q", input)
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
	envHint := defaultTokenEnvNameForConnect(tpl.provider, cfg.BaseURL)
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
	case connectXiaomiTokenPlanCNAlias:
		return "MIMO_TOKEN_PLAN_API_KEY"
	case "xiaomi":
		return "XIAOMI_API_KEY"
	case "volcengine":
		return "VOLCENGINE_API_KEY"
	default:
		return ""
	}
}

func defaultTokenEnvNameForConnect(provider string, baseURL string) string {
	if isXiaomiTokenPlanProvider(provider) || isXiaomiTokenPlanBaseURL(baseURL) {
		return "MIMO_TOKEN_PLAN_API_KEY"
	}
	return defaultTokenEnvName(provider)
}

func defaultConnectAuthType(provider string) model.AuthType {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "minimax":
		return model.AuthBearerToken
	default:
		return model.AuthAPIKey
	}
}

func isXiaomiTokenPlanProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case connectXiaomiTokenPlanCNAlias:
		return true
	default:
		return false
	}
}

func isXiaomiTokenPlanBaseURL(baseURL string) bool {
	switch normalizedConnectBaseURL(baseURL) {
	case normalizedConnectBaseURL(connectXiaomiTokenPlanCNBaseURL):
		return true
	default:
		return false
	}
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
	case "approval":
		return []SlashArgCandidate{
			{Value: "auto-review", Display: "auto-review", Detail: "Use automatic AI approval review"},
			{Value: "manual", Display: "manual", Detail: "Prompt before sensitive requests"},
		}
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
	sandbox := d.stack.SandboxStatus()
	sandboxType = firstNonEmpty(sandbox.ResolvedBackend, sandbox.RequestedBackend, sandboxType)
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
