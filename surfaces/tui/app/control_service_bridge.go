package tuiapp

// control_service_bridge.go bridges the standard control.Service contract into
// Config callback fields. This is the key migration adapter.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	controlcommands "github.com/OnslaughtSnail/caelis/protocol/acp/control/commands"
	controlprompt "github.com/OnslaughtSnail/caelis/protocol/acp/control/prompt"
	"github.com/OnslaughtSnail/caelis/surfaces/statusbar"
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
	id     uint64
	cancel context.CancelFunc
}

type programSenderBoundContextKey struct{}

const programSenderCloseTimeout = 250 * time.Millisecond

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
	s.runCancels = append(s.runCancels, activeRunCancel{id: id, cancel: cancel})
	s.mu.Unlock()
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

func (s *ProgramSender) CancelActiveRuns() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	runCancels := append([]activeRunCancel(nil), s.runCancels...)
	s.runCancels = nil
	s.mu.Unlock()
	for _, run := range runCancels {
		if run.cancel != nil {
			run.cancel()
		}
	}
	return len(runCancels) > 0
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

// ConfigFromControlService populates Config callbacks from a control service.
// When sender is non-nil, its Send field is populated after Program creation
// but before the user can trigger ExecuteLine.
func ConfigFromControlService(service control.Service, sender *ProgramSender, base Config) Config {
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
	var cachedModeLabel string
	var cachedStatusView StatusViewModel
	var statusCacheMu sync.Mutex

	if base.ExecuteLine == nil {
		runExecuteLine := func(sub Submission) executeLineResult {
			runCtx := ctx
			finish := func() {}
			if sender != nil {
				runCtx, finish = sender.beginRunContext(ctx)
			}
			defer finish()
			return executeLineViaControlServiceWithContextResult(runCtx, service, sender, sub)
		}
		base.ExecuteLine = func(sub Submission) TaskResultMsg {
			return runExecuteLine(sub).completion
		}
		base.executeLineCmd = func(sub Submission) tea.Msg {
			return runExecuteLine(sub).commandMessage()
		}
	}
	if base.CanSubmitRunningPrompt == nil {
		base.CanSubmitRunningPrompt = func() bool {
			return controlServiceCanSubmitRunningPrompt(ctx, service)
		}
	}

	if base.RefreshStatus == nil {
		base.RefreshStatus = func() (string, string) {
			status, err := refreshStatusSnapshot(ctx, service)
			if err != nil {
				statusCacheMu.Lock()
				cachedModeLabel = ""
				cachedStatusView = StatusViewModel{}
				statusCacheMu.Unlock()
				return "not configured", ""
			}
			statusCacheMu.Lock()
			cachedModeLabel = strings.TrimSpace(status.Session.ModeLabel)
			cachedStatusView = statusViewModelFromSnapshot(status)
			statusCacheMu.Unlock()
			return statusModelDisplay(status.ModelStatus.Display), statusbar.FormatContextUsage(status.Usage.TotalTokens, status.Usage.ContextWindowTokens)
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

	if base.MentionComplete == nil {
		base.MentionComplete = func(query string, limit int) ([]CompletionCandidate, error) {
			candidates, err := service.CompleteMention(ctx, query, limit)
			if err != nil {
				return nil, err
			}
			out := make([]CompletionCandidate, len(candidates))
			for i, c := range candidates {
				out[i] = CompletionCandidate{
					Value:   c.Value,
					Display: c.Display,
					Detail:  c.Detail,
					Path:    c.Path,
				}
			}
			return out, nil
		}
	}

	if base.FileComplete == nil {
		base.FileComplete = func(query string, limit int) ([]CompletionCandidate, error) {
			candidates, err := service.CompleteFile(ctx, query, limit)
			if err != nil {
				return nil, err
			}
			out := make([]CompletionCandidate, len(candidates))
			for i, c := range candidates {
				out[i] = CompletionCandidate{
					Value:   c.Value,
					Display: c.Display,
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
					Detail:  c.Detail,
					Path:    c.Path,
				}
			}
			return out, nil
		}
	}

	if base.ResumeComplete == nil {
		base.ResumeComplete = func(query string, limit int) ([]ResumeCandidate, error) {
			candidates, err := service.CompleteResume(ctx, query, limit)
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
		base.SlashArgComplete = func(command string, query string, limit int) ([]SlashArgCandidate, error) {
			candidates, err := service.CompleteSlashArg(ctx, command, query, limit)
			if err != nil {
				return nil, err
			}
			out := make([]SlashArgCandidate, len(candidates))
			for i, c := range candidates {
				out[i] = SlashArgCandidate{
					Value:   c.Value,
					Display: c.Display,
					Detail:  c.Detail,
					NoAuth:  c.NoAuth,
				}
			}
			return out, nil
		}
	}

	if base.CancelRunning == nil {
		base.CancelRunning = func() bool {
			requested := sender != nil && sender.CancelActiveRuns()
			err := service.Interrupt(ctx)
			return requested || err == nil
		}
	}

	if base.ToggleMode == nil {
		base.ToggleMode = func() (string, error) {
			status, err := service.CycleSessionMode(ctx)
			if err != nil {
				return "", err
			}
			return modeToggleHint(status), nil
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

func refreshStatusSnapshot(ctx context.Context, service control.Service) (control.StatusSnapshot, error) {
	if lightweight, ok := service.(control.LightweightStatusProvider); ok {
		return lightweight.LightweightStatus(ctx)
	}
	return service.Status(ctx)
}

// ---------------------------------------------------------------------------
// ExecuteLine: the single submission entry point
// ---------------------------------------------------------------------------

func executeLineViaControlService(service control.Service, sender *ProgramSender, sub Submission) TaskResultMsg {
	return executeLineViaControlServiceWithContext(context.Background(), service, sender, sub)
}

func executeLineViaControlServiceWithContext(ctx context.Context, service control.Service, sender *ProgramSender, sub Submission) TaskResultMsg {
	return executeLineViaControlServiceWithContextResult(ctx, service, sender, sub).completion
}

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

func executeLineViaControlServiceWithContextResult(ctx context.Context, service control.Service, sender *ProgramSender, sub Submission) executeLineResult {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}

	router := controlprompt.NewRouter(controlprompt.Config{
		Service: service,
		CommandNames: func(ctx context.Context, service control.Service) []string {
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
	promptResult, err := router.Route(ctx, controlprompt.Request{Submission: control.Submission{
		Text:        sub.Text,
		DisplayText: "",
		Mode:        control.SubmissionMode(sub.Mode),
		Attachments: convertAttachments(sub.Attachments),
	}})
	if err != nil {
		return executeLineResult{completion: TaskResultMsg{Err: err}}
	}
	if privateResult, ok := promptResult.PrivateResult.(executeLineResult); ok {
		return privateResult
	}
	if promptResult.Handled {
		return executeControlPromptResult(ctx, service, sender, promptResult)
	}

	// Normal submission -> control.Service.Submit -> streaming events.
	turn, err := service.Submit(ctx, control.Submission{
		Text:        sub.Text,
		DisplayText: "",
		Mode:        control.SubmissionMode(sub.Mode),
		Attachments: convertAttachments(sub.Attachments),
	})
	if err != nil {
		return executeLineResult{completion: TaskResultMsg{Err: friendlyCommandError("submit", err)}}
	}
	if turn == nil {
		return executeLineResult{completion: TaskResultMsg{ContinueRunning: true, SuppressTurnDivider: true}}
	}
	defer turn.Close()

	send := sender.sendFunc()
	if send != nil {
		return forwardTurnEventStream(ctx, service, turn, sender)
	} else {
		for range turn.Events() {
		}
	}

	return executeLineResult{completion: TaskResultMsg{}}
}

func executeControlPromptResult(ctx context.Context, service control.Service, sender *ProgramSender, result controlprompt.Result) executeLineResult {
	send := sender.sendFunc()
	if result.ClearHistory && send != nil {
		send(ClearHistoryMsg{})
	}
	if len(result.ReplayEvents) > 0 && send != nil {
		if transcriptEvents := projectResumeReplayEvents(result.ReplayEvents); len(transcriptEvents) > 0 {
			send(TranscriptEventsMsg{Events: transcriptEvents})
		}
	}
	if result.SlashResult != nil && send != nil {
		send(SlashCommandResultMsg{Result: *result.SlashResult})
	}
	for _, event := range result.Events {
		if send == nil {
			continue
		}
		send(event)
	}
	if result.StatusUpdate != nil {
		sendStatusUpdate(send, *result.StatusUpdate)
	}
	if result.RefreshCommands {
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
	}
	if result.Turn != nil {
		return runSubagentTurn(ctx, service, sender, result.Turn)
	}
	if result.ContinueRunning {
		return executeLineResult{completion: TaskResultMsg{ContinueRunning: true, SuppressTurnDivider: true}}
	}
	return executeLineResult{completion: TaskResultMsg{SuppressTurnDivider: result.SuppressTurnDivider}}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func sendNotice(send func(tea.Msg), text string) {
	if send != nil {
		send(LogChunkMsg{Chunk: text + "\n"})
	}
}

func appendAgentSlashCommands(service control.Service, commands []string) []string {
	return appendAgentSlashCommandsWithContext(context.Background(), service, commands)
}

func appendAgentSlashCommandsWithContext(ctx context.Context, service control.Service, commands []string) []string {
	ctx = contextOrBackground(ctx)
	if len(commands) == 0 {
		commands = DefaultCommands()
	}
	if status, activeACP := control.ActiveACPStatus(ctx, service); activeACP {
		return acpSlashCommands(status)
	}
	return controlcommands.AppendRegisteredAgentNames(ctx, service, commands)
}

func acpSlashCommands(status control.AgentStatusSnapshot) []string {
	out := []string{"help", "agent", "status", "resume", "model", "exit", "quit"}
	seen := map[string]struct{}{}
	for _, command := range out {
		seen[strings.ToLower(strings.TrimSpace(command))] = struct{}{}
	}
	for _, command := range status.ControllerCommands {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(command, "/")))
		if fields := strings.Fields(name); len(fields) > 0 {
			name = fields[0]
		}
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		out = append(out, name)
		seen[name] = struct{}{}
	}
	return out
}

func refreshAgentSlashCommandsViaSend(service control.Service, send func(tea.Msg)) {
	refreshAgentSlashCommandsViaSendWithContext(context.Background(), service, send)
}

func refreshAgentSlashCommandsViaSendWithContext(ctx context.Context, service control.Service, send func(tea.Msg)) {
	if send == nil {
		return
	}
	send(SetCommandsMsg{Commands: appendAgentSlashCommandsWithContext(ctx, service, DefaultCommands())})
}

func sendStatusUpdate(send func(tea.Msg), status control.StatusSnapshot) {
	if send != nil {
		send(SetStatusMsg{
			Workspace: status.Session.Workspace,
			Model:     statusModelDisplay(status.ModelStatus.Display),
			Context:   statusbar.FormatContextUsage(status.Usage.TotalTokens, status.Usage.ContextWindowTokens),
			ModeLabel: strings.TrimSpace(status.Session.ModeLabel),
			Status:    statusViewModelFromSnapshot(status),
		})
	}
}

func statusModelDisplay(model string) string {
	return normalizeStatusModel(model)
}

func refreshStatusViaSend(service control.Service, send func(tea.Msg)) {
	refreshStatusViaSendWithContext(context.Background(), service, send)
}

func refreshStatusViaSendWithContext(ctx context.Context, service control.Service, send func(tea.Msg)) {
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

func sendApprovalPrompt(ctx context.Context, turn control.Turn, req *approvalPayload, send func(tea.Msg)) {
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

func awaitApprovalPrompt(ctx context.Context, turn control.Turn, req *approvalPayload, responses <-chan PromptResponse, send func(tea.Msg)) {
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
		sendNotice(send, fmt.Sprintf("approval submit failed: %v", err))
	}
}

func approvalDecisionFromPrompt(req *approvalPayload, response PromptResponse) control.ApprovalDecision {
	selected := strings.TrimSpace(response.Line)
	if response.Err != nil || selected == "" {
		return rejectionApprovalDecision(req)
	}
	if req != nil {
		for _, opt := range req.Options {
			if strings.TrimSpace(opt.ID) != selected {
				continue
			}
			return control.ApprovalDecision{
				Outcome:  approvalStatusSelected,
				OptionID: selected,
				Approved: approvalOptionAllows(opt.Kind, opt.Name, opt.ID),
			}
		}
	}
	switch strings.ToLower(selected) {
	case "approve", "allow", "yes", "y":
		return control.ApprovalDecision{Outcome: approvalStatusApproved, Approved: true}
	default:
		return rejectionApprovalDecision(req)
	}
}

func rejectionApprovalDecision(req *approvalPayload) control.ApprovalDecision {
	if req != nil {
		for _, opt := range req.Options {
			if approvalOptionAllows(opt.Kind, opt.Name, opt.ID) {
				continue
			}
			return control.ApprovalDecision{
				Outcome:  approvalStatusSelected,
				OptionID: strings.TrimSpace(opt.ID),
				Approved: false,
			}
		}
	}
	return control.ApprovalDecision{Outcome: approvalStatusRejected, Approved: false}
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
