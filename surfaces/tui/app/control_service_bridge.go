package tuiapp

// control_service_bridge.go bridges focused controlprompt contracts into TUI
// Config callback fields.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/surfaces/internal/promptview"
)

// ProgramSender is set after the tea.Program is created so that the
// ExecuteLine goroutine can send intermediate TUI messages.
type ProgramSender struct {
	Send              func(tea.Msg)
	mu                sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	nextRunID         uint64
	runCancels        []activeRunCancel
	forwarders        sync.WaitGroup
	closed            atomic.Bool
	droppedAfterClose atomic.Uint64
}

type activeRunCancel struct {
	id        uint64
	cancel    context.CancelFunc
	admitting bool
}

type programSenderBoundContextKey struct{}
type programSenderRunContextKey struct{}

const programSenderCloseTimeout = 250 * time.Millisecond

// resumeReplayTranscriptBatchSize bounds one Bubble Tea update while a large
// Session history is projected. Each batch is a separate message so keyboard
// and resize input can be scheduled between replay mutations.
const resumeReplayTranscriptBatchSize = 64

func (s *ProgramSender) sendFunc() func(tea.Msg) {
	if s == nil {
		return nil
	}
	return func(msg tea.Msg) {
		s.SendMsg(msg)
	}
}

func (s *ProgramSender) SendMsg(msg tea.Msg) {
	if s == nil {
		return
	}
	if s.closed.Load() {
		s.droppedAfterClose.Add(1)
		return
	}
	if s.Send != nil {
		s.Send(msg)
	}
}

func (s *ProgramSender) Close() {
	if s == nil {
		return
	}
	if s.closed.Swap(true) {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	runCancels := append([]activeRunCancel(nil), s.runCancels...)
	s.runCancels = nil
	s.mu.Unlock()
	for _, run := range runCancels {
		if run.cancel != nil {
			run.cancel()
		}
	}
	if cancel != nil {
		cancel()
	}
	s.waitForwarders(programSenderCloseTimeout)
}

func (s *ProgramSender) DroppedAfterClose() uint64 {
	if s == nil {
		return 0
	}
	return s.droppedAfterClose.Load()
}

func (s *ProgramSender) bindContext(parent context.Context) context.Context {
	parent = contextOrBackground(parent)
	if s == nil {
		return parent
	}
	if bound, ok := parent.Value(programSenderBoundContextKey{}).(*ProgramSender); ok && bound == s {
		return parent
	}
	return s.observationContext(parent)
}

// observationContext returns the Program-lifetime feed context. Esc interrupt
// must cancel the Host turn, not this observation.
func (s *ProgramSender) observationContext(parent context.Context) context.Context {
	parent = contextOrBackground(parent)
	if s == nil {
		return parent
	}
	if s.closed.Load() {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(parent)
		s.ctx = context.WithValue(s.ctx, programSenderBoundContextKey{}, s)
	}
	return s.ctx
}

func (s *ProgramSender) beginRunContext(parent context.Context) (context.Context, func()) {
	parent = contextOrBackground(parent)
	if s == nil {
		return parent, func() {}
	}
	base := s.bindContext(parent)
	ctx, cancel := context.WithCancel(base)
	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		cancel()
		return ctx, func() {}
	}
	s.nextRunID++
	id := s.nextRunID
	s.runCancels = append(s.runCancels, activeRunCancel{id: id, cancel: cancel, admitting: true})
	s.mu.Unlock()
	ctx = context.WithValue(ctx, programSenderRunContextKey{}, id)
	return ctx, func() {
		s.mu.Lock()
		for i, run := range s.runCancels {
			if run.id == id {
				s.runCancels = append(s.runCancels[:i], s.runCancels[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		cancel()
	}
}

// markRunAdmitted prevents the local Esc fallback from pretending it stopped
// an already-addressable Host Turn. From this point onward only Control's
// exact-target Interrupt may report acceptance; observation remains attached.
func (s *ProgramSender) markRunAdmitted(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	id, _ := ctx.Value(programSenderRunContextKey{}).(uint64)
	if id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runCancels {
		if s.runCancels[i].id == id {
			s.runCancels[i].admitting = false
			return
		}
	}
}

// CancelPendingRuns aborts only requests that have not returned an
// addressable Turn. Live Turn observation is Program-scoped and is never
// detached by this fallback.
func (s *ProgramSender) CancelPendingRuns() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.runCancels))
	for _, run := range s.runCancels {
		if run.admitting && run.cancel != nil {
			cancels = append(cancels, run.cancel)
		}
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels) > 0
}

func (s *ProgramSender) startForwarder(fn func()) bool {
	if s == nil || fn == nil {
		return false
	}
	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		return false
	}
	s.forwarders.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.forwarders.Done()
		fn()
	}()
	return true
}

func (s *ProgramSender) waitForwarders(timeout time.Duration) bool {
	if s == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		s.forwarders.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ControlServices is the consumer-owned set of Control facets required by the
// TUI. ACP onboarding remains separate from this presentation-only aggregate.
type ControlServices interface {
	controlprompt.RouterService
	controlprompt.RunningPromptAdmissionProvider
	WorkspaceDir() string
	Interrupt(context.Context) error
	Connect(context.Context, controlprompt.ConnectConfig) (controlstatus.StatusSnapshot, error)
	DeleteModel(context.Context, string) error
	controlprompt.SessionModeService
	controlprompt.CompletionService
	controlprompt.PluginService
	controlagents.Connector
	controlagents.Disconnector
	agentbinding.Service
}

// ConfigFromControlService populates Config callbacks from Control services.
// When sender is non-nil, its Send field is populated after Program creation
// but before the user can trigger ExecuteLine.
func ConfigFromControlService(service ControlServices, sender *ProgramSender, base Config) Config {
	base.ControlService = service
	if base.StreamTickInterval <= 0 {
		base.StreamTickInterval = streamSmoothingTickIntervalDefault
	}
	ctx := contextOrBackground(base.Context)
	if sender != nil {
		ctx = sender.bindContext(ctx)
		base.Context = ctx
		base.ProgramSender = sender
	}
	base.Commands = appendAgentSlashCommandsWithContext(ctx, service, base.Commands)
	for name, detail := range profileCommandDetailsWithContext(ctx, service) {
		if base.CommandDetails == nil {
			base.CommandDetails = map[string]string{}
		}
		base.CommandDetails[name] = detail
	}
	promptRouterFactory := base.PromptRouterFactory
	var cachedModeLabel string
	var cachedStatusView StatusViewModel
	var cachedStatusUsage controlstatus.StatusUsage
	var statusCacheMu sync.Mutex

	if base.ExecuteLine == nil {
		runExecuteLine := func(sub Submission) executeLineResult {
			runCtx := ctx
			finish := func() {}
			if sender != nil {
				runCtx, finish = sender.beginRunContext(ctx)
			}
			defer finish()
			return executeLineViaControlServiceWithContextResult(runCtx, service, sender, sub, promptRouterFactory)
		}
		base.ExecuteLine = func(sub Submission) TaskResultMsg {
			return runExecuteLine(sub).completion
		}
		base.executeLineCmd = func(sub Submission) tea.Msg {
			return runExecuteLine(sub).commandMessage()
		}
	}
	if base.CanSubmitRunningPrompt == nil {
		base.CanSubmitRunningPrompt = service.CanSubmitRunningPrompt
	}

	if base.RefreshStatus == nil {
		base.RefreshStatus = func() (string, string) {
			status, err := refreshStatusSnapshot(ctx, service)
			if err != nil {
				statusCacheMu.Lock()
				cachedModeLabel = ""
				cachedStatusView = StatusViewModel{}
				cachedStatusUsage = controlstatus.StatusUsage{}
				statusCacheMu.Unlock()
				return "not configured", ""
			}
			statusCacheMu.Lock()
			cachedModeLabel = strings.TrimSpace(status.Session.ModeLabel)
			cachedStatusView = statusViewModelFromSnapshot(status)
			cachedStatusUsage = status.Usage
			statusCacheMu.Unlock()
			return statusModelDisplay(status.ModelStatus.Display), promptview.FormatContextUsage(status.Usage.TotalTokens, status.Usage.ContextWindowTokens)
		}
	}
	if base.RefreshStatusUsage == nil {
		base.RefreshStatusUsage = func() controlstatus.StatusUsage {
			statusCacheMu.Lock()
			defer statusCacheMu.Unlock()
			return cachedStatusUsage
		}
	}
	if base.RefreshStatusView == nil {
		base.RefreshStatusView = func() StatusViewModel {
			statusCacheMu.Lock()
			defer statusCacheMu.Unlock()
			return cachedStatusView
		}
	}
	if base.ModeLabel == nil {
		base.ModeLabel = func() string {
			statusCacheMu.Lock()
			defer statusCacheMu.Unlock()
			return cachedModeLabel
		}
	}

	if base.RefreshWorkspace == nil {
		base.RefreshWorkspace = func() string {
			return service.WorkspaceDir()
		}
	}

	if base.FileComplete == nil {
		base.FileComplete = func(requestCtx context.Context, query string, limit int) ([]CompletionCandidate, error) {
			candidates, err := service.CompleteFile(contextOrBackground(requestCtx), query, limit)
			if err != nil {
				return nil, err
			}
			out := make([]CompletionCandidate, len(candidates))
			for i, c := range candidates {
				out[i] = CompletionCandidate{
					Value:   c.Value,
					Display: c.Display,
					Kind:    c.Kind,
					Detail:  c.Detail,
					Path:    c.Path,
				}
			}
			return out, nil
		}
	}

	if base.SkillComplete == nil {
		base.SkillComplete = func(query string, limit int) ([]CompletionCandidate, error) {
			candidates, err := service.CompleteSkill(ctx, query, limit)
			if err != nil {
				return nil, err
			}
			out := make([]CompletionCandidate, len(candidates))
			for i, c := range candidates {
				out[i] = CompletionCandidate{
					Value:   c.Value,
					Display: c.Display,
					Kind:    c.Kind,
					Detail:  c.Detail,
					Path:    c.Path,
				}
			}
			return out, nil
		}
	}

	if base.ResumeComplete == nil {
		base.ResumeComplete = func(requestCtx context.Context, query string, limit int) ([]ResumeCandidate, error) {
			candidates, err := service.CompleteResume(requestCtx, query, limit)
			if err != nil {
				return nil, err
			}
			out := make([]ResumeCandidate, len(candidates))
			for i, c := range candidates {
				out[i] = ResumeCandidate{
					SessionID: c.SessionID,
					Title:     c.Title,
					Prompt:    c.Prompt,
					Model:     c.Model,
					Workspace: c.Workspace,
					Age:       c.Age,
					UpdatedAt: c.UpdatedAt,
				}
			}
			return out, nil
		}
	}

	if base.SlashArgComplete == nil {
		base.SlashArgComplete = func(requestCtx context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
			candidates, err := service.CompleteSlashArg(contextOrBackground(requestCtx), command, query, limit)
			if err != nil {
				return nil, err
			}
			out := make([]SlashArgCandidate, len(candidates))
			for i, c := range candidates {
				out[i] = SlashArgCandidate{
					Value:                 c.Value,
					Display:               c.Display,
					Detail:                c.Detail,
					NoAuth:                c.NoAuth,
					ModelMetadataComplete: c.ModelMetadataComplete,
					ModelImageInputKnown:  c.ModelImageInputKnown,
				}
			}
			return out, nil
		}
	}

	if base.CancelRunning == nil {
		base.CancelRunning = func() bool {
			// Ask Control to cancel the Host turn. Observation stays on the
			// Program-lifetime context so the cancelled lifecycle can still arrive.
			if service.Interrupt(ctx) == nil {
				return true
			}
			return sender != nil && sender.CancelPendingRuns()
		}
	}

	if base.ToggleMode == nil {
		base.ToggleMode = func() (string, error) {
			status, err := service.CycleSessionMode(ctx)
			if err != nil {
				return "", err
			}
			return controlprompt.ModeToggleHint(status), nil
		}
	}

	if base.ReadClipboardText == nil {
		base.ReadClipboardText = defaultReadClipboardText
	}

	if base.WriteClipboardText == nil {
		base.WriteClipboardText = defaultWriteClipboardText
	}

	if base.PasteClipboardImage == nil {
		base.PasteClipboardImage = defaultPasteClipboardImage
	}

	return base
}

func refreshStatusSnapshot(ctx context.Context, service controlprompt.StatusService) (controlstatus.StatusSnapshot, error) {
	if lightweight, ok := service.(controlprompt.LightweightStatusProvider); ok {
		return lightweight.LightweightStatus(ctx)
	}
	return service.Status(ctx)
}

// ---------------------------------------------------------------------------
// ExecuteLine: the single submission entry point
// ---------------------------------------------------------------------------

type executeLineResult struct {
	completion TaskResultMsg
	queued     bool
}

func (r executeLineResult) commandMessage() tea.Msg {
	if r.queued {
		return nil
	}
	return r.completion
}

func executeLineViaControlServiceWithContextResult(ctx context.Context, service ControlServices, sender *ProgramSender, sub Submission, routerFactory controlprompt.RouterFactory) executeLineResult {
	// Keep the executeLine run context for Route/Submit. Live observation is
	// rebound onto the Program context inside the event-stream forwarder.
	ctx = contextOrBackground(ctx)
	if routerFactory == nil {
		return executeLineResult{completion: TaskResultMsg{Err: fmt.Errorf("control prompt router factory is required")}}
	}

	router := routerFactory(controlprompt.RouterConfig{
		Service: service,
		CommandNames: func(ctx context.Context, service controlprompt.RouterService) []string {
			return appendAgentSlashCommandsWithContext(ctx, service, DefaultCommands())
		},
		PrivateSlashHandler: func(ctx context.Context, req controlprompt.PrivateSlashRequest) (controlprompt.Result, bool, error) {
			result, ok := executeTUIPrivateSlashCommandWithContext(ctx, service, sender, req.Command, req.Args)
			if !ok {
				return controlprompt.Result{}, false, nil
			}
			return controlprompt.Result{
				Handled:             true,
				SuppressTurnDivider: result.completion.SuppressTurnDivider,
				PrivateResult:       result,
			}, true, nil
		},
	})
	if router == nil {
		return executeLineResult{completion: TaskResultMsg{Err: fmt.Errorf("control prompt router factory returned nil")}}
	}
	displayText := strings.TrimSpace(firstNonEmpty(sub.DisplayText, sub.Text))
	promptResult, err := router.Route(ctx, controlprompt.Request{Submission: controlprompt.Submission{
		Text:        sub.Text,
		DisplayText: displayText,
		Mode:        sub.Mode,
		Attachments: convertAttachments(sub.Attachments),
	}})
	if err != nil {
		if sender != nil {
			sender.markRunAdmitted(ctx)
		}
		if errors.Is(err, context.Canceled) {
			return executeLineResult{completion: TaskResultMsg{Interrupted: true}}
		}
		return executeLineResult{completion: TaskResultMsg{Err: err}}
	}
	if privateResult, ok := promptResult.PrivateResult.(executeLineResult); ok {
		if sender != nil {
			sender.markRunAdmitted(ctx)
		}
		return privateResult
	}
	if promptResult.Handled {
		if sender != nil {
			sender.markRunAdmitted(ctx)
		}
		return executeControlPromptResult(ctx, service, sender, promptResult)
	}

	// Router-declined slash input falls back to a normal prompt submission.
	submitText := strings.TrimSpace(sub.Text)
	turn, err := service.Submit(ctx, controlprompt.Submission{
		Text:        submitText,
		DisplayText: displayText,
		Mode:        sub.Mode,
		Attachments: convertAttachments(sub.Attachments),
	})
	if sender != nil {
		sender.markRunAdmitted(ctx)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return executeLineResult{completion: TaskResultMsg{Interrupted: true}}
		}
		return executeLineResult{completion: TaskResultMsg{Err: controlprompt.FriendlyCommandError("submit", err)}}
	}
	if turn == nil {
		return executeLineResult{completion: TaskResultMsg{ContinueRunning: true, SuppressTurnDivider: true}}
	}
	defer turn.Close()

	send := sender.sendFunc()
	if send != nil {
		return forwardTurnEventStream(ctx, turn, sender)
	} else {
		for range turn.Events() {
		}
	}

	return executeLineResult{completion: TaskResultMsg{}}
}

func executeControlPromptResult(ctx context.Context, service ControlServices, sender *ProgramSender, result controlprompt.Result) executeLineResult {
	send := sender.sendFunc()
	if result.Reconnect != nil {
		defer result.Reconnect.Close()
		if send != nil {
			send(SessionReconnectMsg{State: result.Reconnect.State()})
		}
		if err := streamReconnectBackfill(ctx, result.Reconnect, send); err != nil {
			return executeLineResult{completion: TaskResultMsg{Err: controlprompt.FriendlyCommandError("resume session feed", err)}}
		}
	} else if result.ClearHistory && send != nil {
		send(ClearHistoryMsg{})
	}
	for _, event := range result.Events {
		if send == nil {
			continue
		}
		if event.Kind == eventstream.KindNotice {
			sendControlSlashNotice(send, event.Notice)
			continue
		}
		send(event)
	}
	if result.SlashResult != nil && send != nil {
		send(SlashCommandResultMsg{Result: *result.SlashResult})
	}
	if result.StatusUpdate != nil {
		sendStatusUpdate(send, *result.StatusUpdate)
	}
	if result.RefreshStatus && send != nil {
		send(statusRefreshRequestMsg{})
	}
	if result.RefreshCommands {
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
	}
	if result.Reconnect != nil {
		for _, event := range result.Reconnect.BootstrapEvents() {
			if send == nil {
				continue
			}
			send(event)
			if req := approvalPayloadFromACPEvent(event); req != nil {
				sendApprovalPrompt(ctx, result.Reconnect, req, send)
			}
		}
		state := result.Reconnect.State()
		if state.Run.Active || state.Approval.Active != nil {
			return forwardSessionReconnectEventStream(ctx, result.Reconnect, sender)
		}
		return executeLineResult{completion: TaskResultMsg{SuppressTurnDivider: result.SuppressTurnDivider}}
	}
	if result.Turn != nil {
		return runSubagentTurn(ctx, sender, result.Turn)
	}
	if result.ContinueRunning {
		return executeLineResult{completion: TaskResultMsg{ContinueRunning: true, SuppressTurnDivider: true}}
	}
	return executeLineResult{completion: TaskResultMsg{SuppressTurnDivider: result.SuppressTurnDivider}}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func appendAgentSlashCommands(service ControlServices, commands []string) []string {
	return appendAgentSlashCommandsWithContext(context.Background(), service, commands)
}

func appendAgentSlashCommandsWithContext(ctx context.Context, service controlprompt.RouterService, commands []string) []string {
	ctx = contextOrBackground(ctx)
	if len(commands) == 0 {
		commands = DefaultCommands()
	}
	var bindingStatus agentbinding.Status
	if bindings, ok := service.(agentbinding.Service); ok {
		bindingStatus, _ = bindings.AgentBindingStatus(ctx)
	}
	commands = agentbinding.ProjectBoundDirectNames(commands, bindingStatus)
	status, err := service.AgentStatus(ctx)
	if err == nil {
		if strings.EqualFold(strings.TrimSpace(status.ControllerKind), string(session.ControllerKindACP)) {
			commands = controlprompt.WithoutNames(commands, "compact")
		}
		commands = controlagents.AppendRunNames(commands, tuiDirectAgentRuns(status), nil)
	}
	return commands
}

func tuiDirectAgentRuns(status controlprompt.AgentStatusSnapshot) []controlagents.Run {
	runs := make([]controlagents.Run, 0, len(status.Participants))
	for _, participant := range status.Participants {
		runs = append(runs, controlagents.DirectRunFromParticipant(participant.Label, participant.Kind, participant.Role, participant.Source))
	}
	return runs
}

func refreshAgentSlashCommandsViaSend(service ControlServices, send func(tea.Msg)) {
	refreshAgentSlashCommandsViaSendWithContext(context.Background(), service, send)
}

func refreshAgentSlashCommandsViaSendWithContext(ctx context.Context, service controlprompt.RouterService, send func(tea.Msg)) {
	if send == nil {
		return
	}
	send(SetCommandsMsg{
		Commands: appendAgentSlashCommandsWithContext(ctx, service, DefaultCommands()),
		Details:  profileCommandDetailsWithContext(ctx, service),
	})
}

func profileCommandDetailsWithContext(ctx context.Context, service controlprompt.RouterService) map[string]string {
	if service == nil {
		return nil
	}
	ctx = contextOrBackground(ctx)
	details := map[string]string{}
	if bindings, ok := service.(agentbinding.Service); ok {
		if status, err := bindings.AgentBindingStatus(ctx); err == nil {
			for _, handle := range status.Handles {
				if !agentbinding.IsDirectRunDefinition(handle.Definition) || !agentbinding.IsBound(handle) {
					continue
				}
				details[string(handle.Definition.Handle)] = subagentProfileCommandDetail(handle)
			}
		}
	}
	if status, err := service.AgentStatus(ctx); err == nil {
		for _, run := range tuiDirectAgentRuns(status) {
			if !run.Addressable {
				continue
			}
			agent, handle, ok := controlagents.ParseRunName(run.Name)
			if !ok {
				continue
			}
			details[run.Name] = fmt.Sprintf("Continue /%s as %s", agent, handle)
		}
	}
	if len(details) == 0 {
		return nil
	}
	return details
}

func sendStatusUpdate(send func(tea.Msg), status controlstatus.StatusSnapshot) {
	if send != nil {
		send(SetStatusMsg{
			Workspace:            status.Session.Workspace,
			Model:                statusModelDisplay(status.ModelStatus.Display),
			Context:              promptview.FormatContextUsage(status.Usage.TotalTokens, status.Usage.ContextWindowTokens),
			TotalTokens:          status.Usage.TotalTokens,
			ContextWindowTokens:  status.Usage.ContextWindowTokens,
			HasUsage:             statusContextUsageAvailable(status.Usage),
			UsageReplace:         status.Usage.ContextUsageReplace,
			UsageControllerEpoch: strings.TrimSpace(status.Usage.ContextUsageControllerEpoch),
			ModeLabel:            strings.TrimSpace(status.Session.ModeLabel),
			Status:               statusViewModelFromSnapshot(status),
		})
	}
}

func statusContextUsageAvailable(usage controlstatus.StatusUsage) bool {
	return usage.ContextUsageAvailable
}

func statusModelDisplay(model string) string {
	return normalizeStatusModel(model)
}

func refreshStatusViaSend(service controlprompt.StatusService, send func(tea.Msg)) {
	refreshStatusViaSendWithContext(context.Background(), service, send)
}

func refreshStatusViaSendWithContext(ctx context.Context, service controlprompt.StatusService, send func(tea.Msg)) {
	ctx = contextOrBackground(ctx)
	status, err := service.Status(ctx)
	if err != nil {
		return
	}
	sendStatusUpdate(send, status)
}

func approvalCommandPreview(raw map[string]any) string {
	if len(raw) == 0 {
		return ""
	}
	if preview := approvalKnownInputPreview(raw); preview != "" {
		return preview
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return compactString(string(data), 240)
}

func approvalRawInputFromJSON(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil
	}
	return decoded
}

func sendApprovalPrompt(ctx context.Context, turn controlprompt.Turn, req *approvalPayload, send func(tea.Msg)) {
	if turn == nil || req == nil || send == nil {
		return
	}
	responses := make(chan PromptResponse, 1)
	send(approvalToPromptRequest(req, responses))
	go awaitApprovalPrompt(ctx, turn, req, responses, send)
}

func isAutomaticApprovalEvent(req *approvalPayload) bool {
	if req == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(req.DecisionSource), "auto-review") ||
		strings.TrimSpace(req.ReviewStatus) != "" ||
		strings.TrimSpace(req.ReviewID) != ""
}

func automaticApprovalReviewDisplayText(req *approvalPayload) string {
	if req == nil {
		return ""
	}
	switch req.ReviewStatus {
	case approvalReviewStatusApproved, approvalReviewStatusDenied, approvalReviewStatusTimedOut, approvalReviewStatusFailed:
		return firstNonEmpty(strings.TrimSpace(req.ReviewText), "Automatic approval review "+strings.TrimSpace(req.ReviewStatus))
	default:
		if text := strings.TrimSpace(req.ReviewText); text != "" {
			return text
		}
		return ""
	}
}

func awaitApprovalPrompt(ctx context.Context, turn controlprompt.Turn, req *approvalPayload, responses <-chan PromptResponse, send func(tea.Msg)) {
	ctx = contextOrBackground(ctx)
	var response PromptResponse
	select {
	case <-ctx.Done():
		return
	case next, ok := <-responses:
		if !ok {
			return
		}
		response = next
	}
	decision := approvalDecisionFromPrompt(req, response)
	if err := turn.SubmitApproval(ctx, decision); err != nil {
		if send != nil {
			send(LogChunkMsg{Chunk: fmt.Sprintf("approval submit failed: %v\n", err)})
		}
	}
}

func approvalDecisionFromPrompt(req *approvalPayload, response PromptResponse) controlprompt.ApprovalDecision {
	requestID := eventstream.ApprovalRequestID("")
	if req != nil {
		requestID = req.RequestID
	}
	selected := strings.TrimSpace(response.Line)
	if response.Err != nil || selected == "" {
		return rejectionApprovalDecision(req)
	}
	if req != nil {
		for _, opt := range req.Options {
			if strings.TrimSpace(opt.ID) != selected {
				continue
			}
			return controlprompt.ApprovalDecision{
				RequestID: req.RequestID,
				Outcome:   approvalStatusSelected,
				OptionID:  selected,
				Approved:  approvalOptionAllows(opt.Kind, opt.Name, opt.ID),
			}
		}
	}
	switch strings.ToLower(selected) {
	case "approve", "allow", "yes", "y":
		return controlprompt.ApprovalDecision{RequestID: requestID, Outcome: approvalStatusApproved, Approved: true}
	default:
		return rejectionApprovalDecision(req)
	}
}

func rejectionApprovalDecision(req *approvalPayload) controlprompt.ApprovalDecision {
	if req != nil {
		for _, opt := range req.Options {
			if approvalOptionAllows(opt.Kind, opt.Name, opt.ID) {
				continue
			}
			return controlprompt.ApprovalDecision{
				RequestID: req.RequestID,
				Outcome:   approvalStatusSelected,
				OptionID:  strings.TrimSpace(opt.ID),
				Approved:  false,
			}
		}
	}
	requestID := eventstream.ApprovalRequestID("")
	if req != nil {
		requestID = req.RequestID
	}
	return controlprompt.ApprovalDecision{RequestID: requestID, Outcome: approvalStatusRejected, Approved: false}
}

func approvalOptionAllows(kind string, name string, id string) bool {
	parts := []string{strings.ToLower(strings.TrimSpace(kind)), strings.ToLower(strings.TrimSpace(name)), strings.ToLower(strings.TrimSpace(id))}
	for _, part := range parts {
		if strings.HasPrefix(part, "allow") || strings.HasPrefix(part, "approve") {
			return true
		}
	}
	return false
}
