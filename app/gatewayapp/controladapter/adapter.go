package controladapter

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	acpprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type Adapter struct {
	mu                  sync.Mutex
	stack               *RuntimeStack
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

func NewAdapter(ctx context.Context, stack *RuntimeStack, preferredSessionID string, bindingKey string, modelText string) (*Adapter, error) {
	if stack == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: stack is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	driver := newAdapterForStack(stack, bindingKey, modelText)
	if preferredSessionID = strings.TrimSpace(preferredSessionID); preferredSessionID != "" {
		if driver.stack.Session.StartFn == nil {
			return nil, missingRuntimeDependency("start session")
		}
		activeSession, err := driver.stack.Session.StartFn(ctx, preferredSessionID, driver.bindingKey)
		if err != nil {
			return nil, err
		}
		driver.bindSession(ctx, activeSession)
	}
	return driver, nil
}

// NewAdapterForSession constructs an adapter bound to an already resolved
// session. It is used by ACP prompt routing, where the session lookup has
// already applied the client workspace and must not be repeated via StartFn.
func NewAdapterForSession(ctx context.Context, stack *RuntimeStack, activeSession session.Session, bindingKey string, modelText string) (*Adapter, error) {
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
	driver := newAdapterForStack(stack, bindingKey, modelText)
	driver.bindSession(ctx, activeSession)
	return driver, nil
}

func newAdapterForStack(stack *RuntimeStack, bindingKey string, modelText string) *Adapter {
	key := firstNonEmpty(strings.TrimSpace(bindingKey), "cli-tui")
	return &Adapter{
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
}

func (d *Adapter) bindSession(ctx context.Context, activeSession session.Session) {
	activeSession = session.CloneSession(activeSession)
	d.session = activeSession
	d.hasSession = true
	d.refreshSessionDisplay(ctx, activeSession)
}

func (d *Adapter) gateway() (GatewayService, error) {
	if d == nil || d.stack == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: stack is required")
	}
	if d.stack.Gateway.ServiceFn == nil {
		return nil, missingRuntimeDependency("gateway")
	}
	gw := d.stack.Gateway.ServiceFn()
	if gw == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: gateway is unavailable")
	}
	return gw, nil
}

func (d *Adapter) SubscribeStream(ctx context.Context, env eventstream.Envelope) (<-chan eventstream.Envelope, bool) {
	gw, err := d.gateway()
	if err != nil {
		return nil, false
	}
	req, ok := streamRequestFromACPEvent(env)
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

	out := make(chan eventstream.Envelope, 32)
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
			for _, projected := range acpprojector.ProjectStreamFrame(req, stream.CloneFrame(*frame)) {
				select {
				case out <- projected:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, true
}

func streamRequestFromACPEvent(env eventstream.Envelope) (acpprojector.StreamRequest, bool) {
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		return acpprojector.StreamRequest{}, false
	}
	status := strings.TrimSpace(stringFromPtr(update.Status))
	if status != schema.ToolStatusInProgress {
		return acpprojector.StreamRequest{}, false
	}
	meta := mergeMeta(update.Meta, env.Meta)
	toolName := streamToolNameFromACPUpdate(meta, update)
	if toolName == "" {
		return acpprojector.StreamRequest{}, false
	}
	taskID := firstNonEmpty(
		metaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "task_id"),
		metaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "internal_task_id"),
	)
	displayTerminalID := acpTerminalID(update.Content)
	terminalID := firstNonEmpty(
		metaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "terminal_id"),
		displayTerminalID,
	)
	if taskID == "" && terminalID == "" {
		return acpprojector.StreamRequest{}, false
	}
	scope := gateway.EventScope(env.Scope)
	if scope == "" {
		scope = gateway.EventScopeMain
	}
	req := acpprojector.StreamRequest{
		HandleID: strings.TrimSpace(env.HandleID),
		RunID:    strings.TrimSpace(env.RunID),
		TurnID:   strings.TrimSpace(env.TurnID),
		SessionRef: session.SessionRef{SessionID: firstNonEmpty(
			strings.TrimSpace(env.SessionID),
			metaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "session_id"),
		)},
		CallID:   strings.TrimSpace(update.ToolCallID),
		ToolName: toolName,
		RawInput: anyMap(update.RawInput),
		Ref: stream.Ref{
			SessionID: firstNonEmpty(
				strings.TrimSpace(env.SessionID),
				metaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "session_id"),
			),
			TaskID:     taskID,
			TerminalID: terminalID,
		},
		DisplayTerminalID: firstNonEmpty(displayTerminalID, strings.TrimSpace(update.ToolCallID)),
		Cursor: stream.Cursor{
			Output: int64FromAny(metaAny(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "output_cursor")),
		},
		Origin: &gateway.EventOrigin{
			Scope:         scope,
			ScopeID:       strings.TrimSpace(env.ScopeID),
			Actor:         strings.TrimSpace(env.Actor),
			ParticipantID: strings.TrimSpace(env.ParticipantID),
		},
		Actor:         strings.TrimSpace(env.Actor),
		Scope:         scope,
		ParticipantID: strings.TrimSpace(env.ParticipantID),
	}
	if req.SessionRef.SessionID == "" || req.Ref.SessionID == "" || req.CallID == "" || req.ToolName == "" {
		return acpprojector.StreamRequest{}, false
	}
	return req, true
}

func streamToolNameFromACPUpdate(meta map[string]any, update schema.ToolCallUpdate) string {
	if name := strings.TrimSpace(metaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, gateway.EventMetaRuntimeTool, gateway.EventMetaRuntimeToolName)); name != "" {
		return name
	}
	if name := streamToolNameFromTitle(stringFromPtr(update.Title)); name != "" {
		return name
	}
	return strings.TrimSpace(stringFromPtr(update.Kind))
}

func streamToolNameFromTitle(title string) string {
	title = strings.TrimSpace(title)
	fields := strings.Fields(title)
	if len(fields) == 0 {
		return ""
	}
	switch strings.ToUpper(strings.Trim(fields[0], ":")) {
	case "RUN_COMMAND", "SPAWN":
		return strings.ToUpper(strings.Trim(fields[0], ":"))
	}
	return ""
}

func acpTerminalID(content []schema.ToolCallContent) string {
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
			return terminalID
		}
	}
	return ""
}

func mergeMeta(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := map[string]any{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overlay {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = mergeMeta(baseMap, overlayMap)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func metaAny(values map[string]any, path ...string) any {
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

func metaString(values map[string]any, path ...string) string {
	text, _ := metaAny(values, path...).(string)
	return strings.TrimSpace(text)
}

func metaBool(values map[string]any, path ...string) bool {
	value, _ := metaAny(values, path...).(bool)
	return value
}

func anyMap(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok {
		out := make(map[string]any, len(mapped))
		for key, value := range mapped {
			out[key] = value
		}
		return out
	}
	return nil
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func stringFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
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

func (d *Adapter) WorkspaceDir() string {
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

func (d *Adapter) ensureSession(ctx context.Context) (session.Session, error) {
	if activeSession, ok := d.currentSession(); ok {
		return activeSession, nil
	}
	if d == nil || d.stack == nil {
		return session.Session{}, fmt.Errorf("app/gatewayapp/controladapter: stack is unavailable")
	}
	if d.stack.Session.StartFn == nil {
		return session.Session{}, missingRuntimeDependency("start session")
	}
	activeSession, err := d.stack.Session.StartFn(ctx, "", d.bindingKey)
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

func (d *Adapter) currentSession() (session.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasSession {
		return session.Session{}, false
	}
	return d.session, true
}

func (d *Adapter) activeACPControllerStatus(ctx context.Context) (controller.ControllerStatus, bool, error) {
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

func (d *Adapter) Submit(ctx context.Context, submission Submission) (Turn, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	rawInput := strings.TrimSpace(submission.Text)
	displayInput := strings.TrimSpace(submission.DisplayText)
	if displayInput == rawInput {
		displayInput = ""
	}
	contentParts, err := contentPartsFromSubmission(rawInput, submission.Attachments, d.WorkspaceDir())
	if err != nil {
		return nil, err
	}
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	if isBuiltInControllerSession(activeSession) && activeKernelTurnForSession(gw.ActiveTurns(), activeSession.SessionRef) {
		err := gw.SubmitActiveTurn(ctx, gateway.SubmitActiveTurnRequest{
			SessionRef:   activeSession.SessionRef,
			Kind:         gateway.SubmissionKindConversation,
			Text:         rawInput,
			DisplayText:  displayInput,
			ContentParts: contentParts,
			Metadata: map[string]any{
				"submission_mode": string(submission.Mode),
			},
		})
		if err == nil {
			return nil, nil
		}
		if !isNoActiveRunError(err) {
			return nil, err
		}
	}
	result, err := gw.BeginTurn(ctx, gateway.BeginTurnRequest{
		SessionRef:   activeSession.SessionRef,
		Input:        rawInput,
		DisplayInput: displayInput,
		ContentParts: contentParts,
		Surface:      d.bindingKey,
		Metadata: map[string]any{
			"submission_mode": string(submission.Mode),
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
	return &gatewayTurn{handle: result.Handle}, nil
}

func activeKernelTurnForSession(active []gateway.ActiveTurnState, ref session.SessionRef) bool {
	kind, ok := activeTurnKindForSession(active, ref)
	if !ok {
		return false
	}
	return kind == "" || strings.EqualFold(kind, string(gateway.ActiveTurnKindKernel))
}

func activeTurnKindForSession(active []gateway.ActiveTurnState, ref session.SessionRef) (string, bool) {
	state, ok := activeTurnStateForSession(active, ref)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(string(state.Kind)), true
}

func activeTurnStateForSession(active []gateway.ActiveTurnState, ref session.SessionRef) (gateway.ActiveTurnState, bool) {
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return gateway.ActiveTurnState{}, false
	}
	for _, item := range active {
		if strings.TrimSpace(item.SessionRef.SessionID) == sessionID {
			return item, true
		}
	}
	return gateway.ActiveTurnState{}, false
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
	var gwErr *gateway.Error
	return errors.As(err, &gwErr) && gwErr.Code == gateway.CodeNoActiveRun
}

func (d *Adapter) Interrupt(ctx context.Context) error {
	cancelCommand := d.activeCommandInterrupt()
	if cancelCommand != nil {
		cancelCommand()
	}
	activeSession, ok := d.currentSession()
	if !ok {
		if cancelCommand != nil {
			return nil
		}
		return fmt.Errorf("app/gatewayapp/controladapter: no active session")
	}
	gw, err := d.gateway()
	if err != nil {
		return err
	}
	if err := gw.Interrupt(ctx, gateway.InterruptRequest{
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

func (d *Adapter) beginInterruptibleCommand(ctx context.Context) (context.Context, func()) {
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

func (d *Adapter) activeCommandInterrupt() context.CancelFunc {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.activeCommandCancel
}

func (d *Adapter) NewSession(ctx context.Context) (SessionSnapshot, error) {
	if d.stack.Session.StartFn == nil {
		return SessionSnapshot{}, missingRuntimeDependency("start session")
	}
	activeSession, err := d.stack.Session.StartFn(ctx, "", d.bindingKey)
	if err != nil {
		return SessionSnapshot{}, err
	}
	d.mu.Lock()
	d.session = activeSession
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, activeSession)
	return sessionSnapshotFromSession(activeSession), nil
}

func (d *Adapter) ResumeSession(ctx context.Context, sessionID string) (SessionSnapshot, error) {
	gw, err := d.gateway()
	if err != nil {
		return SessionSnapshot{}, err
	}
	result, err := gw.ResumeSession(ctx, gateway.ResumeSessionRequest{
		AppName:    d.stack.Session.AppName,
		UserID:     d.stack.Session.UserID,
		Workspace:  d.stack.Session.Workspace,
		SessionID:  strings.TrimSpace(sessionID),
		BindingKey: d.bindingKey,
		Binding: gateway.BindingDescriptor{
			Surface: d.bindingKey,
			Owner:   d.stack.Session.AppName,
		},
	})
	if err != nil {
		return SessionSnapshot{}, err
	}
	d.mu.Lock()
	d.session = result.Session
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, result.Session)
	return sessionSnapshotFromSession(result.Session), nil
}

func sessionSnapshotFromSession(activeSession session.Session) SessionSnapshot {
	return SessionSnapshot{SessionID: strings.TrimSpace(activeSession.SessionID)}
}

func (d *Adapter) ListSessions(ctx context.Context, limit int) ([]ResumeCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	ctx, cancel := completionContext(ctx, resumeCompletionTimeout)
	defer cancel()
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	result, err := gw.ListSessions(ctx, gateway.ListSessionsRequest{
		AppName:      d.stack.Session.AppName,
		UserID:       d.stack.Session.UserID,
		WorkspaceKey: d.stack.Session.Workspace.Key,
		Limit:        limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ResumeCandidate, 0, len(result.Sessions))
	for _, session := range result.Sessions {
		candidate := enrichResumeCandidate(ctx, d.stack.Session.Store, session)
		if strings.TrimSpace(candidate.Prompt) == "" && strings.TrimSpace(candidate.Title) == "" {
			continue
		}
		out = append(out, candidate)
	}
	return out, nil
}

func (d *Adapter) ReplayEvents(ctx context.Context) ([]eventstream.Envelope, error) {
	activeSession, ok := d.currentSession()
	if !ok {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: no active session")
	}
	gw, err := d.gateway()
	if err != nil {
		return nil, err
	}
	result, err := gw.ReplayEvents(ctx, gateway.ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: d.bindingKey,
	})
	if err != nil {
		return nil, err
	}
	return result.Events, nil
}

func (d *Adapter) Compact(ctx context.Context) error {
	activeSession, ok := d.currentSession()
	if !ok {
		return fmt.Errorf("app/gatewayapp/controladapter: no active session")
	}
	if d.stack.Session.CompactFn == nil {
		return missingRuntimeDependency("compact")
	}
	return d.stack.Session.CompactFn(ctx, activeSession.SessionRef)
}

func (d *Adapter) ListAgents(ctx context.Context, limit int) ([]AgentCandidate, error) {
	limit = normalizeCompletionLimit(limit)
	return d.agentCatalog(limit), nil
}

func (d *Adapter) AgentStatus(ctx context.Context) (AgentStatusSnapshot, error) {
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
	state, err := gw.ControlPlaneState(ctx, gateway.ControlPlaneStateRequest{
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

func agentParticipantSnapshot(participant gateway.ParticipantState) AgentParticipantSnapshot {
	return AgentParticipantSnapshot{
		ID:        strings.TrimSpace(participant.ID),
		Label:     strings.TrimSpace(firstNonEmpty(participant.Label, participant.ID)),
		AgentName: strings.TrimSpace(firstNonEmpty(participant.AgentName, participant.Label, participant.ID)),
		Kind:      string(participant.Kind),
		Role:      string(participant.Role),
		SessionID: strings.TrimSpace(participant.SessionID),
	}
}

func (d *Adapter) AddAgent(ctx context.Context, target string) (AgentStatusSnapshot, error) {
	return d.AddAgentWithOptions(ctx, target, AgentAddOptions{})
}

func (d *Adapter) AddAgentWithOptions(ctx context.Context, target string, opts AgentAddOptions) (AgentStatusSnapshot, error) {
	if opts.Custom != nil {
		if d.stack.Agent.RegisterCustomFn == nil {
			return AgentStatusSnapshot{}, missingRuntimeDependency("custom ACP agent")
		}
		if err := d.stack.Agent.RegisterCustomFn(ctx, *opts.Custom); err != nil {
			return AgentStatusSnapshot{}, err
		}
		return d.AgentStatus(ctx)
	}
	if opts.Install {
		var finish func()
		ctx, finish = d.beginInterruptibleCommand(ctx)
		defer finish()
	}
	if d.stack.Agent.RegisterBuiltinWithOptionsFn == nil {
		return AgentStatusSnapshot{}, missingRuntimeDependency("builtin ACP agent")
	}
	if err := d.stack.Agent.RegisterBuiltinWithOptionsFn(ctx, target, RegisterBuiltinACPAgentOptions{Install: opts.Install}); err != nil {
		if opts.Install && errors.Is(ctx.Err(), context.Canceled) {
			return AgentStatusSnapshot{}, context.Canceled
		}
		return AgentStatusSnapshot{}, err
	}
	return d.AgentStatus(ctx)
}

func (d *Adapter) RemoveAgent(ctx context.Context, target string) (AgentStatusSnapshot, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	status, err := d.AgentStatus(ctx)
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	if strings.EqualFold(strings.TrimSpace(status.ControllerKind), string(session.ControllerKindACP)) {
		return AgentStatusSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: an ACP agent is the active controller; run /agent use local before removing registered agents")
	}
	if d.stack.Agent.UnregisterFn == nil {
		return AgentStatusSnapshot{}, missingRuntimeDependency("ACP agent unregister")
	}
	if err := d.stack.Agent.UnregisterFn(target); err != nil {
		return AgentStatusSnapshot{}, err
	}
	return d.AgentStatus(ctx)
}

func (d *Adapter) HandoffAgent(ctx context.Context, target string) (AgentStatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return AgentStatusSnapshot{}, err
	}
	target = strings.TrimSpace(target)
	req := gateway.HandoffControllerRequest{
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

func (d *Adapter) defaultDisplays() (string, string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.defaultModelText, d.defaultSessionMode, d.defaultSandboxType
}

func (d *Adapter) refreshSessionDisplay(ctx context.Context, activeSession session.Session) {
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
