package tuiapp

// driver_bridge.go bridges the TUI driver contract into the legacy Config
// callback fields. This is the key migration adapter.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

// ProgramSender is set after the tea.Program is created so that the
// ExecuteLine goroutine can send intermediate TUI messages.
type ProgramSender struct {
	Send              func(tea.Msg)
	mu                sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	forwarders        sync.WaitGroup
	closed            atomic.Bool
	droppedAfterClose atomic.Uint64
}

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
	s.mu.Unlock()
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
	if s.closed.Load() {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(parent)
	}
	return s.ctx
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

type streamDriver interface {
	SubscribeStream(context.Context, kernel.EventEnvelope) (<-chan kernel.EventEnvelope, bool)
}

// ConfigFromDriver populates legacy Config callbacks from a TUI driver.
// sender must be non-nil; its Send field is populated after Program creation
// but before the user can trigger ExecuteLine.
func ConfigFromDriver(driver tuidriver.Driver, sender *ProgramSender, base Config) Config {
	base.Driver = driver
	if base.StreamTickInterval <= 0 {
		base.StreamTickInterval = streamSmoothingTickIntervalDefault
	}
	ctx := contextOrBackground(base.Context)
	if sender != nil {
		ctx = sender.bindContext(ctx)
		base.Context = ctx
		base.ProgramSender = sender
	}
	base.Commands = appendAgentSlashCommandsWithContext(ctx, driver, base.Commands)
	var cachedModeLabel string
	var cachedStatusView StatusViewModel

	if base.ExecuteLine == nil {
		base.ExecuteLine = func(sub Submission) TaskResultMsg {
			return executeLineViaDriverWithContext(ctx, driver, sender, sub)
		}
	}
	if base.CanSubmitRunningPrompt == nil {
		base.CanSubmitRunningPrompt = func() bool {
			return driverCanSubmitRunningPrompt(ctx, driver)
		}
	}

	if base.RefreshStatus == nil {
		base.RefreshStatus = func() (string, string) {
			status, err := driver.Status(ctx)
			if err != nil {
				cachedModeLabel = ""
				cachedStatusView = StatusViewModel{}
				return "not configured", ""
			}
			cachedModeLabel = strings.TrimSpace(status.ModeLabel)
			cachedStatusView = statusViewModelFromSnapshot(status)
			return statusModelDisplay(status.Model), formatContextUsageStatus(status.TotalTokens, status.ContextWindowTokens)
		}
	}
	if base.RefreshStatusView == nil {
		base.RefreshStatusView = func() StatusViewModel {
			return cachedStatusView
		}
	}
	if base.ModeLabel == nil {
		base.ModeLabel = func() string {
			return cachedModeLabel
		}
	}

	if base.RefreshWorkspace == nil {
		base.RefreshWorkspace = func() string {
			return driver.WorkspaceDir()
		}
	}

	if base.MentionComplete == nil {
		base.MentionComplete = func(query string, limit int) ([]CompletionCandidate, error) {
			candidates, err := driver.CompleteMention(ctx, query, limit)
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
			candidates, err := driver.CompleteFile(ctx, query, limit)
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
			candidates, err := driver.CompleteSkill(ctx, query, limit)
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
			candidates, err := driver.CompleteResume(ctx, query, limit)
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
			candidates, err := driver.CompleteSlashArg(ctx, command, query, limit)
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
			err := driver.Interrupt(ctx)
			return err == nil
		}
	}

	if base.ToggleMode == nil {
		base.ToggleMode = func() (string, error) {
			status, err := driver.CycleSessionMode(ctx)
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

	return base
}

// ---------------------------------------------------------------------------
// ExecuteLine: the single submission entry point
// ---------------------------------------------------------------------------

func executeLineViaDriver(driver tuidriver.Driver, sender *ProgramSender, sub Submission) TaskResultMsg {
	return executeLineViaDriverWithContext(context.Background(), driver, sender, sub)
}

func executeLineViaDriverWithContext(ctx context.Context, driver tuidriver.Driver, sender *ProgramSender, sub Submission) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	text := strings.TrimSpace(sub.Text)

	// Slash command dispatch.
	if isDispatchableSlashCommandWithContext(ctx, driver, text) {
		return dispatchSlashCommandWithContext(ctx, driver, sender, text)
	}
	if strings.HasPrefix(text, "@") {
		return dispatchMentionCommandWithContext(ctx, driver, sender, text)
	}

	// Normal submission → Driver.Submit → streaming events.
	turn, err := driver.Submit(ctx, tuidriver.Submission{
		Text:        sub.Text,
		DisplayText: "",
		Mode:        tuidriver.SubmissionMode(sub.Mode),
		Attachments: convertAttachments(sub.Attachments),
	})
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("submit", err)}
	}
	if turn == nil {
		return TaskResultMsg{ContinueRunning: true, SuppressTurnDivider: true}
	}
	defer turn.Close()

	send := sender.sendFunc()
	if send != nil {
		forwardGatewayTurnEvents(ctx, driver, turn, sender)
	} else {
		for range turn.Events() {
		}
	}

	return TaskResultMsg{}
}

const gatewayNarrativeBatchInterval = 16 * time.Millisecond

type gatewayNarrativeBatcher struct {
	pending *kernel.EventEnvelope
	key     string
}

func forwardGatewayTurnEvents(ctx context.Context, driver tuidriver.Driver, turn tuidriver.Turn, sender *ProgramSender) {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	if turn == nil || send == nil {
		return
	}
	events := turn.Events()
	if events == nil {
		return
	}
	ticker := time.NewTicker(gatewayNarrativeBatchInterval)
	defer ticker.Stop()

	var batcher gatewayNarrativeBatcher
	for events != nil {
		select {
		case <-ctx.Done():
			batcher.flush(send)
			return
		case <-ticker.C:
			batcher.flush(send)
		case env, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if batcher.enqueue(env, send) {
				continue
			}
			send(env)
			startTerminalStreamForwarder(ctx, driver, env, sender)
			if isApprovalGatewayEvent(env.Event.Kind) {
				if !isAutomaticApprovalEvent(env.Event.ApprovalPayload) {
					sendApprovalPrompt(ctx, turn, env.Event.ApprovalPayload, send)
				}
			}
		}
	}
	batcher.flush(send)
}

func (b *gatewayNarrativeBatcher) enqueue(env kernel.EventEnvelope, send func(tea.Msg)) bool {
	key, ok := gatewayNarrativeBatchKey(env)
	if !ok {
		b.flush(send)
		return false
	}
	if b.pending == nil {
		copy := cloneGatewayNarrativeEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	if b.key != key {
		b.flush(send)
		copy := cloneGatewayNarrativeEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	mergeGatewayNarrativeEnvelope(b.pending, env)
	return true
}

func (b *gatewayNarrativeBatcher) flush(send func(tea.Msg)) {
	if b == nil || b.pending == nil {
		return
	}
	if send != nil {
		send(*b.pending)
	}
	b.pending = nil
	b.key = ""
}

func gatewayNarrativeBatchKey(env kernel.EventEnvelope) (string, bool) {
	if env.Err != nil || env.Event.Kind != kernel.EventKindAssistantMessage || env.Event.Narrative == nil {
		return "", false
	}
	payload := env.Event.Narrative
	if payload.Role != kernel.NarrativeRoleAssistant || payload.Final || strings.TrimSpace(payload.Visibility) != "ui_only" {
		return "", false
	}
	stream := ""
	switch {
	case payload.ReasoningText != "" && payload.Text == "":
		stream = "reasoning"
	case payload.Text != "" && payload.ReasoningText == "":
		stream = "answer"
	default:
		return "", false
	}
	scope := string(payload.Scope)
	if env.Event.Origin != nil && env.Event.Origin.Scope != "" {
		scope = string(env.Event.Origin.Scope)
	}
	scopeID := gatewayEventScopeID(env.Event)
	return strings.Join([]string{
		strings.TrimSpace(env.Event.HandleID),
		strings.TrimSpace(env.Event.RunID),
		strings.TrimSpace(env.Event.TurnID),
		strings.TrimSpace(env.Event.SessionRef.SessionID),
		strings.TrimSpace(scope),
		strings.TrimSpace(scopeID),
		strings.TrimSpace(payload.ParticipantID),
		strings.TrimSpace(payload.Actor),
		strings.TrimSpace(payload.UpdateType),
		stream,
	}, "\x00"), true
}

func cloneGatewayNarrativeEnvelope(env kernel.EventEnvelope) kernel.EventEnvelope {
	out := env
	if env.Event.Narrative != nil {
		payload := *env.Event.Narrative
		out.Event.Narrative = &payload
	}
	return out
}

func mergeGatewayNarrativeEnvelope(dst *kernel.EventEnvelope, src kernel.EventEnvelope) {
	if dst == nil || dst.Event.Narrative == nil || src.Event.Narrative == nil {
		return
	}
	dst.Cursor = src.Cursor
	dst.Event.OccurredAt = src.Event.OccurredAt
	dst.Event.Narrative.Text += src.Event.Narrative.Text
	dst.Event.Narrative.ReasoningText += src.Event.Narrative.ReasoningText
}

func startTerminalStreamForwarder(ctx context.Context, driver tuidriver.Driver, env kernel.EventEnvelope, sender *ProgramSender) {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	if send == nil {
		return
	}
	streamer, ok := driver.(streamDriver)
	if !ok {
		return
	}
	events, ok := streamer.SubscribeStream(ctx, env)
	if !ok || events == nil {
		return
	}
	start := func() {
		ticker := time.NewTicker(gatewayNarrativeBatchInterval)
		defer ticker.Stop()
		var batcher gatewayTerminalBatcher
		for events != nil {
			select {
			case <-ctx.Done():
				batcher.flush(send)
				return
			case <-ticker.C:
				batcher.flush(send)
			case terminalEnv, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if batcher.enqueue(terminalEnv, send) {
					continue
				}
				send(terminalEnv)
			}
		}
		batcher.flush(send)
	}
	if sender != nil {
		sender.startForwarder(start)
		return
	}
	go start()
}

type gatewayTerminalBatcher struct {
	pending *kernel.EventEnvelope
	key     string
}

func (b *gatewayTerminalBatcher) enqueue(env kernel.EventEnvelope, send func(tea.Msg)) bool {
	key, ok := gatewayTerminalBatchKey(env)
	if !ok {
		b.flush(send)
		return false
	}
	if b.pending == nil {
		copy := cloneGatewayTerminalEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	if b.key != key {
		b.flush(send)
		copy := cloneGatewayTerminalEnvelope(env)
		b.pending = &copy
		b.key = key
		return true
	}
	mergeGatewayTerminalEnvelope(b.pending, env)
	return true
}

func (b *gatewayTerminalBatcher) flush(send func(tea.Msg)) {
	if b == nil || b.pending == nil {
		return
	}
	if send != nil {
		send(*b.pending)
	}
	b.pending = nil
	b.key = ""
}

func gatewayTerminalBatchKey(env kernel.EventEnvelope) (string, bool) {
	if env.Err != nil || env.Event.Kind != kernel.EventKindToolResult || env.Event.ToolResult == nil {
		return "", false
	}
	payload := env.Event.ToolResult
	if !rawBool(payload.RawOutput, "running") {
		return "", false
	}
	text := rawString(payload.RawOutput, "text")
	if text == "" {
		return "", false
	}
	return strings.Join([]string{
		strings.TrimSpace(env.Event.HandleID),
		strings.TrimSpace(env.Event.RunID),
		strings.TrimSpace(env.Event.TurnID),
		strings.TrimSpace(env.Event.SessionRef.SessionID),
		strings.TrimSpace(payload.CallID),
		strings.TrimSpace(payload.ToolName),
		rawString(payload.RawOutput, "task_id"),
		rawString(payload.RawOutput, "terminal_id"),
		rawString(payload.RawOutput, "stream"),
	}, "\x00"), true
}

func cloneGatewayTerminalEnvelope(env kernel.EventEnvelope) kernel.EventEnvelope {
	out := env
	if env.Event.ToolResult != nil {
		payload := *env.Event.ToolResult
		payload.RawInput = cloneAnyMap(payload.RawInput)
		payload.RawOutput = cloneAnyMap(payload.RawOutput)
		out.Event.ToolResult = &payload
	}
	if env.Event.Meta != nil {
		out.Event.Meta = cloneAnyMap(env.Event.Meta)
	}
	return out
}

func mergeGatewayTerminalEnvelope(dst *kernel.EventEnvelope, src kernel.EventEnvelope) {
	if dst == nil || dst.Event.ToolResult == nil || src.Event.ToolResult == nil {
		return
	}
	dst.Cursor = src.Cursor
	dst.Event.OccurredAt = src.Event.OccurredAt
	dstPayload := dst.Event.ToolResult
	srcPayload := src.Event.ToolResult
	if dstPayload.RawOutput == nil {
		dstPayload.RawOutput = map[string]any{}
	}
	if text := rawString(srcPayload.RawOutput, "text"); text != "" {
		existing := rawString(dstPayload.RawOutput, "text")
		if strings.EqualFold(strings.TrimSpace(dstPayload.ToolName), "BASH") {
			dstPayload.RawOutput["text"] = appendDeltaStreamChunk(existing, text)
		} else {
			dstPayload.RawOutput["text"] = mergeSubagentStreamChunk(existing, text)
		}
	}
	for _, key := range []string{"running", "state", "stdout_cursor", "stderr_cursor", "exit_code"} {
		if value, ok := srcPayload.RawOutput[key]; ok {
			dstPayload.RawOutput[key] = value
		}
	}
}

func rawString(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	switch value := values[key].(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return ""
	}
}

func rawBool(values map[string]any, key string) bool {
	if len(values) == 0 {
		return false
	}
	switch value := values[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

func cloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// ---------------------------------------------------------------------------
// Slash command dispatch
// ---------------------------------------------------------------------------

func dispatchSlashCommand(driver tuidriver.Driver, sender *ProgramSender, text string) TaskResultMsg {
	return dispatchSlashCommandWithContext(context.Background(), driver, sender, text)
}

func dispatchSlashCommandWithContext(ctx context.Context, driver tuidriver.Driver, sender *ProgramSender, text string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	cmd, args := splitSlash(text)
	send := sender.sendFunc()

	switch cmd {
	case "help":
		return slashHelpWithContext(ctx, driver, send)
	case "agent":
		return slashAgentWithContext(ctx, driver, send, args)
	case "new":
		return slashNewWithContext(ctx, driver, send)
	case "resume":
		return slashResumeWithContext(ctx, driver, send, args)
	case "status":
		return slashStatusWithContext(ctx, driver, send)
	case "connect":
		return slashConnectWithContext(ctx, driver, send, args)
	case "model":
		return slashModelWithContext(ctx, driver, send, args)
	case "approval":
		return slashApprovalWithContext(ctx, driver, send, args)
	case "sandbox":
		return slashSandboxWithContext(ctx, driver, send, args)
	case "compact":
		return slashCompactWithContext(ctx, driver, send, args)
	case "exit", "quit":
		return TaskResultMsg{ExitNow: true}
	default:
		return slashDynamicAgentWithContext(ctx, driver, sender, cmd, args)
	}
}

func isDispatchableSlashCommand(driver tuidriver.Driver, text string) bool {
	return isDispatchableSlashCommandWithContext(context.Background(), driver, text)
}

func activeACPAgentStatus(ctx context.Context, driver tuidriver.Driver) (tuidriver.AgentStatusSnapshot, bool) {
	if driver == nil {
		return tuidriver.AgentStatusSnapshot{}, false
	}
	status, err := driver.AgentStatus(contextOrBackground(ctx))
	if err != nil {
		return tuidriver.AgentStatusSnapshot{}, false
	}
	return status, strings.EqualFold(strings.TrimSpace(status.ControllerKind), "acp")
}

func isCoreLocalSlashCommand(cmd string) bool {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "help", "agent", "status", "resume", "model", "approval", "exit", "quit":
		return true
	default:
		return false
	}
}

func driverCanSubmitRunningPrompt(ctx context.Context, driver tuidriver.Driver) bool {
	if driver == nil {
		return true
	}
	status, err := driver.AgentStatus(contextOrBackground(ctx))
	if err != nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(status.ControllerKind), "acp") {
		return false
	}
	if !status.HasActiveTurn {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(status.ActiveTurnKind), "kernel")
}

func isDispatchableSlashCommandWithContext(ctx context.Context, driver tuidriver.Driver, text string) bool {
	cmd, _ := splitSlash(text)
	if cmd == "" {
		return false
	}
	if _, activeACP := activeACPAgentStatus(ctx, driver); activeACP {
		return isCoreLocalSlashCommand(cmd)
	}
	if _, ok := lookupSlashCommandSpec(cmd); ok {
		return true
	}
	return isRegisteredAgentCommandWithContext(ctx, driver, cmd)
}

func slashHelp(send func(tea.Msg)) TaskResultMsg {
	sendNotice(send, defaultHelpText())
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashHelpWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg)) TaskResultMsg {
	sendNotice(send, helpTextForCommands(appendAgentSlashCommandsWithContext(ctx, driver, DefaultCommands())))
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashDynamicAgent(driver tuidriver.Driver, send func(tea.Msg), agent string, prompt string) TaskResultMsg {
	return slashDynamicAgentWithContext(context.Background(), driver, &ProgramSender{Send: send}, agent, prompt)
}

func slashDynamicAgentWithContext(ctx context.Context, driver tuidriver.Driver, sender *ProgramSender, agent string, prompt string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	agent = strings.ToLower(strings.TrimSpace(agent))
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		if isRegisteredAgentCommand(driver, agent) {
			sendNotice(send, fmt.Sprintf("usage: /%s <prompt>", agent))
		} else {
			sendNotice(send, fmt.Sprintf("unknown command: /%s\nrun /help to see supported commands", agent))
		}
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	turn, err := driver.StartAgentSubagent(ctx, agent, prompt)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("/"+agent, err)}
	}
	if turn == nil {
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	defer turn.Close()
	if send != nil {
		forwardGatewayTurnEvents(ctx, driver, turn, sender)
	} else {
		for range turn.Events() {
		}
	}
	return TaskResultMsg{}
}

func isRegisteredAgentCommand(driver tuidriver.Driver, agent string) bool {
	return isRegisteredAgentCommandWithContext(context.Background(), driver, agent)
}

func isRegisteredAgentCommandWithContext(ctx context.Context, driver tuidriver.Driver, agent string) bool {
	ctx = contextOrBackground(ctx)
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return false
	}
	agents, err := driver.ListAgents(ctx, 200)
	if err != nil {
		return false
	}
	for _, item := range agents {
		if strings.EqualFold(strings.TrimSpace(item.Name), agent) {
			return true
		}
	}
	return false
}

func dispatchMentionCommand(driver tuidriver.Driver, sender *ProgramSender, text string) TaskResultMsg {
	return dispatchMentionCommandWithContext(context.Background(), driver, sender, text)
}

func dispatchMentionCommandWithContext(ctx context.Context, driver tuidriver.Driver, sender *ProgramSender, text string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	handle, prompt := splitFirst(strings.TrimSpace(text))
	handle = strings.TrimPrefix(strings.TrimSpace(handle), "@")
	if handle == "" || strings.TrimSpace(prompt) == "" {
		sendNotice(send, "usage: @handle <prompt>")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	turn, err := driver.ContinueSubagent(ctx, handle, prompt)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("@"+handle, err)}
	}
	if turn == nil {
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	defer turn.Close()
	if send != nil {
		forwardGatewayTurnEvents(ctx, driver, turn, sender)
	} else {
		for range turn.Events() {
		}
	}
	return TaskResultMsg{}
}

func slashAgent(driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	return slashAgentWithContext(context.Background(), driver, send, args)
}

func slashAgentWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	sub, rest := splitFirst(strings.TrimSpace(args))
	switch sub {
	case "", "help":
		sendNotice(send, agentHelpText())
		return TaskResultMsg{SuppressTurnDivider: true}
	case "list":
		agents, err := driver.ListAgents(ctx, 20)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("agent list", err)}
		}
		status, _ := driver.AgentStatus(ctx)
		sendNotice(send, formatAgentList(agents, status))
		return TaskResultMsg{SuppressTurnDivider: true}
	case "status":
		sendNotice(send, "usage: /agent list | add <builtin> | install <adapter> | use <agent|local> | remove <agent>")
		return TaskResultMsg{SuppressTurnDivider: true}
	case "add":
		addArgs, ok := parseAgentAddArgs(rest)
		if !ok || addArgs.Target == "" {
			sendNotice(send, "usage: /agent add <name> | /agent add custom <name> -- <command> [args...]")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		status, err := driver.AddAgentWithOptions(ctx, addArgs.Target, tuidriver.AgentAddOptions{
			Install: addArgs.Install,
			Custom:  addArgs.Custom,
		})
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("agent add", err)}
		}
		if addArgs.Custom != nil {
			sendNotice(send, fmt.Sprintf("custom agent registered: %s", addArgs.Target))
		} else if addArgs.Install {
			sendNotice(send, fmt.Sprintf("agent registered with local adapter: %s", addArgs.Target))
		} else {
			sendNotice(send, fmt.Sprintf("agent registered: %s", addArgs.Target))
		}
		sendNotice(send, formatAgentStatusSnapshot(status))
		refreshAgentSlashCommandsViaSendWithContext(ctx, driver, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	case "install":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /agent install <adapter>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		command := agentInstallCommandForDisplay(ctx, driver, target)
		callID := sendAgentInstallToolCall(send, target, command)
		status, err := driver.AddAgentWithOptions(ctx, target, tuidriver.AgentAddOptions{Install: true})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				sendAgentInstallToolResult(send, callID, command, kernel.ToolStatusInterrupted, false, agentInstallErrorOutput(err))
				return TaskResultMsg{Interrupted: true, SuppressTurnDivider: true}
			}
			sendAgentInstallToolResult(send, callID, command, kernel.ToolStatusFailed, true, agentInstallErrorOutput(err))
			return TaskResultMsg{Err: friendlyCommandError("agent install", err)}
		}
		sendAgentInstallToolResult(send, callID, command, kernel.ToolStatusCompleted, false, "")
		sendNotice(send, fmt.Sprintf("agent installed and registered: %s", target))
		sendNotice(send, formatAgentStatusSnapshot(status))
		refreshAgentSlashCommandsViaSendWithContext(ctx, driver, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	case "remove":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /agent remove <agent>\nrun /agent list to inspect registered agents")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		status, err := driver.RemoveAgent(ctx, target)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("agent remove", err)}
		}
		sendNotice(send, fmt.Sprintf("agent unregistered: %s", target))
		sendNotice(send, formatAgentStatusSnapshot(status))
		refreshAgentSlashCommandsViaSendWithContext(ctx, driver, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	case "use":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /agent use <agent|local>\nrun /agent list for registered agents")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		status, err := driver.HandoffAgent(ctx, target)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("agent use", err)}
		}
		sendNotice(send, formatAgentStatusSnapshot(status))
		if current, err := driver.Status(ctx); err == nil {
			sendStatusUpdate(send, current)
		}
		refreshAgentSlashCommandsViaSendWithContext(ctx, driver, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, "usage: /agent list | add <builtin> | install <adapter> | use <agent|local> | remove <agent>")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func agentInstallCommandForDisplay(ctx context.Context, driver tuidriver.Driver, target string) string {
	target = strings.TrimSpace(target)
	if driver != nil {
		if candidates, err := driver.CompleteSlashArg(ctx, "agent install", target, 20); err == nil {
			for _, candidate := range candidates {
				if !strings.EqualFold(strings.TrimSpace(candidate.Value), target) {
					continue
				}
				if detail := strings.TrimSpace(candidate.Detail); detail != "" {
					return detail
				}
			}
		}
	}
	if target == "" {
		return "npm install"
	}
	return "npm install " + target
}

func sendAgentInstallToolCall(send func(tea.Msg), target string, command string) string {
	if send == nil {
		return ""
	}
	callID := "agent-install-" + strings.ToLower(strings.ReplaceAll(strings.TrimSpace(target), " ", "-")) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	send(kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindToolCall,
		OccurredAt: time.Now(),
		ToolCall: &kernel.ToolCallPayload{
			CallID:   callID,
			ToolName: "BASH",
			Status:   kernel.ToolStatusRunning,
			Scope:    kernel.EventScopeMain,
			RawInput: map[string]any{"command": strings.TrimSpace(command)},
		},
	}})
	return callID
}

func sendAgentInstallToolResult(send func(tea.Msg), callID string, command string, status kernel.ToolStatus, isErr bool, output string) {
	if send == nil || strings.TrimSpace(callID) == "" {
		return
	}
	rawOutput := map[string]any{
		"running": false,
		"state":   string(status),
	}
	output = strings.TrimSpace(output)
	if output != "" {
		if isErr {
			rawOutput["stderr"] = output
		} else {
			rawOutput["stdout"] = output
		}
	}
	send(kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindToolResult,
		OccurredAt: time.Now(),
		ToolResult: &kernel.ToolResultPayload{
			CallID:    callID,
			ToolName:  "BASH",
			Status:    status,
			Scope:     kernel.EventScopeMain,
			RawInput:  map[string]any{"command": strings.TrimSpace(command)},
			RawOutput: rawOutput,
			Error:     isErr,
		},
	}})
}

func agentInstallErrorOutput(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "User interrupt"
	}
	text := strings.TrimSpace(err.Error())
	if idx := strings.Index(text, "\n"); idx >= 0 {
		if out := strings.TrimSpace(text[idx+1:]); out != "" {
			return out
		}
	}
	return text
}

func slashNew(driver tuidriver.Driver, send func(tea.Msg)) TaskResultMsg {
	return slashNewWithContext(context.Background(), driver, send)
}

func slashNewWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg)) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	session, err := driver.NewSession(ctx)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("new session", err)}
	}
	if send != nil {
		send(ClearHistoryMsg{})
	}
	sendNotice(send, fmt.Sprintf("new session: %s", session.SessionID))
	refreshStatusViaSendWithContext(ctx, driver, send)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashResume(driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	return slashResumeWithContext(context.Background(), driver, send, args)
}

func slashResumeWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	sessionID := strings.TrimSpace(args)
	if sessionID == "" {
		// List available sessions.
		candidates, err := driver.ListSessions(ctx, 10)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("list sessions", err)}
		}
		if len(candidates) == 0 {
			sendNotice(send, "no sessions available to resume")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		var lines []string
		lines = append(lines, "available sessions:")
		for _, c := range candidates {
			line := fmt.Sprintf("  %s", c.SessionID)
			if c.Prompt != "" {
				line += fmt.Sprintf("  %s", c.Prompt)
			}
			if c.Age != "" {
				line += fmt.Sprintf("  (%s)", c.Age)
			}
			lines = append(lines, line)
		}
		sendNotice(send, strings.Join(lines, "\n"))
		return TaskResultMsg{SuppressTurnDivider: true}
	}

	// Resume specific session.
	if _, err := driver.ResumeSession(ctx, sessionID); err != nil {
		return TaskResultMsg{Err: friendlyCommandError("resume session", err)}
	}
	if send != nil {
		send(ClearHistoryMsg{})
	}

	// Replay historical events into transcript.
	events, err := driver.ReplayEvents(ctx)
	if err != nil {
		sendNotice(send, fmt.Sprintf("warning: replay failed: %v", err))
	} else if len(events) > 0 {
		for _, env := range resumeTranscriptReplayEvents(events) {
			if send != nil {
				send(env)
			}
		}
	}

	refreshStatusViaSendWithContext(ctx, driver, send)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func resumeTranscriptReplayEvents(events []kernel.EventEnvelope) []kernel.EventEnvelope {
	if len(events) == 0 {
		return nil
	}
	incompleteTurns := resumeIncompleteTurnIDs(events)
	out := make([]kernel.EventEnvelope, 0, len(events))
	for _, env := range events {
		if shouldReplayEventInTUIResume(env.Event) || shouldReplayInterruptedTurnEvent(env.Event, incompleteTurns) {
			out = append(out, env)
		}
	}
	return out
}

func resumeIncompleteTurnIDs(events []kernel.EventEnvelope) map[string]bool {
	hasUser := map[string]bool{}
	hasAssistantFinal := map[string]bool{}
	for _, env := range events {
		turnID := strings.TrimSpace(env.Event.TurnID)
		if turnID == "" || gatewayEventScope(env.Event) != ACPProjectionMain {
			continue
		}
		switch env.Event.Kind {
		case kernel.EventKindUserMessage:
			if strings.TrimSpace(gatewayUserText(env.Event)) != "" {
				hasUser[turnID] = true
			}
		case kernel.EventKindAssistantMessage:
			if completedResumeAssistant(env.Event) {
				hasAssistantFinal[turnID] = true
			}
		}
	}
	out := map[string]bool{}
	for turnID := range hasUser {
		if !hasAssistantFinal[turnID] {
			out[turnID] = true
		}
	}
	return out
}

func shouldReplayEventInTUIResume(event kernel.Event) bool {
	switch event.Kind {
	case kernel.EventKindUserMessage:
		return strings.TrimSpace(gatewayUserText(event)) != ""
	case kernel.EventKindAssistantMessage:
		payload := event.Narrative
		if payload == nil {
			return false
		}
		switch payload.Role {
		case kernel.NarrativeRoleUser:
			return strings.TrimSpace(payload.Text) != ""
		case kernel.NarrativeRoleAssistant:
			return replayableResumeAssistant(event)
		default:
			return false
		}
	default:
		return false
	}
}

func replayableResumeAssistant(event kernel.Event) bool {
	payload := event.Narrative
	if payload == nil || payload.Role != kernel.NarrativeRoleAssistant {
		return false
	}
	if !payload.Final {
		return false
	}
	if strings.TrimSpace(payload.Text) == "" && strings.TrimSpace(payload.ReasoningText) == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(payload.Visibility), "ui_only") {
		return false
	}
	scope := payload.Scope
	if scope == "" && event.Origin != nil {
		scope = event.Origin.Scope
	}
	return scope == "" || scope == kernel.EventScopeMain
}

func completedResumeAssistant(event kernel.Event) bool {
	if !replayableResumeAssistant(event) {
		return false
	}
	payload := event.Narrative
	return payload == nil || !strings.EqualFold(strings.TrimSpace(payload.Visibility), "mirror")
}

func shouldReplayInterruptedTurnEvent(event kernel.Event, incompleteTurns map[string]bool) bool {
	turnID := strings.TrimSpace(event.TurnID)
	if turnID == "" || !incompleteTurns[turnID] {
		return false
	}
	if gatewayEventScope(event) != ACPProjectionMain {
		return false
	}
	switch event.Kind {
	case kernel.EventKindPlanUpdate, kernel.EventKindToolCall, kernel.EventKindToolResult:
		return true
	default:
		return false
	}
}

func slashStatus(driver tuidriver.Driver, send func(tea.Msg)) TaskResultMsg {
	return slashStatusWithContext(context.Background(), driver, send)
}

func slashStatusWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg)) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	status, err := driver.Status(ctx)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("status", err)}
	}
	sendNotice(send, formatStatusSnapshot(status))
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashConnect(driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	return slashConnectWithContext(context.Background(), driver, send, args)
}

func slashConnectWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	cfg := parseConnectArgs(args)
	if cfg.Provider == "" || cfg.Model == "" {
		sendNotice(send, "usage: /connect\nrun /connect to open the guided setup wizard")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Provider), "codefree") {
		sendNotice(send, "opening CodeFree OAuth in your browser and waiting for authentication...")
	}
	status, err := driver.Connect(ctx, cfg)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("connect", err)}
	}
	sendNotice(send, fmt.Sprintf("connected: %s", status.Model))
	sendStatusUpdate(send, status)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashModel(driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	return slashModelWithContext(context.Background(), driver, send, args)
}

func slashModelWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	sub, rest := splitFirst(strings.TrimSpace(args))
	_, activeACP := activeACPAgentStatus(ctx, driver)
	switch sub {
	case "use":
		alias, reasoning := parseModelUseArgs(rest)
		if alias == "" {
			if activeACP {
				sendNotice(send, "usage: /model use <model> [effort]")
			} else {
				sendNotice(send, "usage: /model use <alias>")
			}
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		status, err := driver.UseModel(ctx, alias, reasoning)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("model use", err)}
		}
		if strings.TrimSpace(reasoning) != "" {
			sendNotice(send, fmt.Sprintf("model switched to: %s (reasoning: %s)", status.Model, reasoning))
		} else {
			sendNotice(send, fmt.Sprintf("model switched to: %s", status.Model))
		}
		sendStatusUpdate(send, status)
	case "del", "delete", "rm":
		if activeACP {
			sendNotice(send, "usage: /model use <model> [effort]")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		alias := strings.TrimSpace(rest)
		if alias == "" {
			sendNotice(send, "usage: /model del <alias>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		if err := driver.DeleteModel(ctx, alias); err != nil {
			return TaskResultMsg{Err: friendlyCommandError("model delete", err)}
		}
		sendNotice(send, fmt.Sprintf("model deleted: %s", alias))
		refreshStatusViaSendWithContext(ctx, driver, send)
	default:
		if activeACP {
			sendNotice(send, "usage: /model use <model> [effort]")
		} else {
			sendNotice(send, "usage: /model use|del <alias>")
		}
	}
	return TaskResultMsg{SuppressTurnDivider: true}
}

func parseModelUseArgs(args string) (alias string, reasoning string) {
	alias, rest := splitFirst(strings.TrimSpace(args))
	return strings.TrimSpace(alias), strings.TrimSpace(rest)
}

func slashSandbox(driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	return slashSandboxWithContext(context.Background(), driver, send, args)
}

func slashSandboxWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	backend := strings.TrimSpace(args)
	if backend == "" {
		status, err := driver.Status(ctx)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("sandbox", err)}
		}
		lines := []string{
			fmt.Sprintf("sandbox requested: %s", firstNonEmpty(strings.TrimSpace(status.SandboxRequestedBackend), "-")),
			fmt.Sprintf("sandbox resolved: %s", firstNonEmpty(strings.TrimSpace(status.SandboxResolvedBackend), firstNonEmpty(strings.TrimSpace(status.SandboxType), "-"))),
			fmt.Sprintf("session mode: %s", firstNonEmpty(strings.TrimSpace(status.SessionMode), "auto-review")),
			fmt.Sprintf("route: %s", firstNonEmpty(strings.TrimSpace(status.Route), "-")),
		}
		if status.FallbackReason != "" {
			lines = append(lines, fmt.Sprintf("fallback: %s", status.FallbackReason))
		}
		if status.SandboxInstallHint != "" {
			lines = append(lines, fmt.Sprintf("install: %s", status.SandboxInstallHint))
		}
		if status.SandboxAutoReviewDisabled {
			lines = append(lines, "warning: Auto-Review is disabled until a sandbox backend is available")
		}
		if status.HostExecution || status.FullAccessMode {
			lines = append(lines, "warning: commands may execute on the host with reduced isolation")
		}
		sendNotice(send, strings.Join(lines, "\n"))
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	status, err := driver.SetSandboxBackend(ctx, backend)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("sandbox", err)}
	}
	sendNotice(send, fmt.Sprintf("sandbox backend: %s", status.SandboxType))
	sendStatusUpdate(send, status)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashApprovalWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	mode := strings.TrimSpace(args)
	if mode == "" {
		status, err := driver.Status(ctx)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("approval", err)}
		}
		sendNotice(send, fmt.Sprintf("approval mode: %s", firstNonEmpty(strings.TrimSpace(status.SessionMode), "auto-review")))
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	switch strings.ToLower(mode) {
	case "auto-review", "auto_review", "autoreview", "manual":
	default:
		sendNotice(send, "usage: /approval [auto-review|manual]")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	status, err := driver.SetSessionMode(ctx, mode)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("approval", err)}
	}
	sendNotice(send, modeToggleHint(status))
	sendStatusUpdate(send, status)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashCompact(driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	return slashCompactWithContext(context.Background(), driver, send, args)
}

func slashCompactWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if strings.TrimSpace(args) != "" {
		sendNotice(send, "usage: /compact")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	if err := driver.Compact(ctx); err != nil {
		return TaskResultMsg{Err: friendlyCommandError("compact", err)}
	}
	sendNotice(send, "compaction completed")
	return TaskResultMsg{SuppressTurnDivider: true}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func sendNotice(send func(tea.Msg), text string) {
	if send != nil {
		send(LogChunkMsg{Chunk: text + "\n"})
	}
}

func appendAgentSlashCommands(driver tuidriver.Driver, commands []string) []string {
	return appendAgentSlashCommandsWithContext(context.Background(), driver, commands)
}

func appendAgentSlashCommandsWithContext(ctx context.Context, driver tuidriver.Driver, commands []string) []string {
	ctx = contextOrBackground(ctx)
	if len(commands) == 0 {
		commands = DefaultCommands()
	}
	if status, activeACP := activeACPAgentStatus(ctx, driver); activeACP {
		return acpSlashCommands(status)
	}
	out := append([]string(nil), commands...)
	seen := map[string]struct{}{}
	for _, command := range out {
		seen[strings.ToLower(strings.TrimSpace(command))] = struct{}{}
	}
	agents, err := driver.ListAgents(ctx, 200)
	if err != nil {
		return out
	}
	for _, agent := range agents {
		name := strings.ToLower(strings.TrimSpace(agent.Name))
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

func acpSlashCommands(status tuidriver.AgentStatusSnapshot) []string {
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

func refreshAgentSlashCommandsViaSend(driver tuidriver.Driver, send func(tea.Msg)) {
	refreshAgentSlashCommandsViaSendWithContext(context.Background(), driver, send)
}

func refreshAgentSlashCommandsViaSendWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg)) {
	if send == nil {
		return
	}
	send(SetCommandsMsg{Commands: appendAgentSlashCommandsWithContext(ctx, driver, DefaultCommands())})
}

func sendStatusUpdate(send func(tea.Msg), status tuidriver.StatusSnapshot) {
	if send != nil {
		send(SetStatusMsg{
			Workspace: status.Workspace,
			Model:     statusModelDisplay(status.Model),
			Context:   formatContextUsageStatus(status.TotalTokens, status.ContextWindowTokens),
			ModeLabel: strings.TrimSpace(status.ModeLabel),
			Status:    statusViewModelFromSnapshot(status),
		})
	}
}

func statusModelDisplay(model string) string {
	return normalizeStatusModel(model)
}

func formatContextUsageStatus(totalTokens int, contextWindow int) string {
	if contextWindow <= 0 {
		return ""
	}
	if totalTokens < 0 {
		totalTokens = 0
	}
	percent := 0
	if contextWindow > 0 {
		percent = int(float64(totalTokens)*100/float64(contextWindow) + 0.5)
		if percent < 0 {
			percent = 0
		}
	}
	return fmt.Sprintf("%s/%s(%d%%)", formatCompactTokenCount(totalTokens), formatCompactTokenCount(contextWindow), percent)
}

func formatCompactTokenCount(tokens int) string {
	if tokens < 1000 {
		return strconv.Itoa(max(tokens, 0))
	}
	value := float64(tokens) / 1000.0
	text := fmt.Sprintf("%.1fk", value)
	return strings.Replace(text, ".0k", "k", 1)
}

func refreshStatusViaSend(driver tuidriver.Driver, send func(tea.Msg)) {
	refreshStatusViaSendWithContext(context.Background(), driver, send)
}

func refreshStatusViaSendWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg)) {
	ctx = contextOrBackground(ctx)
	status, err := driver.Status(ctx)
	if err != nil {
		return
	}
	sendStatusUpdate(send, status)
}

func approvalCommandPreview(raw map[string]any) string {
	if len(raw) == 0 {
		return ""
	}
	for _, key := range []string{"command", "cmd", "file_path", "path", "query", "url", "pattern", "text"} {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return compactString(strings.TrimSpace(key)+": "+value, 240)
		}
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

func sendApprovalPrompt(ctx context.Context, turn tuidriver.Turn, req *kernel.ApprovalPayload, send func(tea.Msg)) {
	if turn == nil || req == nil || send == nil {
		return
	}
	responses := make(chan PromptResponse, 1)
	send(approvalToPromptRequest(req, responses))
	go awaitApprovalPrompt(ctx, turn, req, responses, send)
}

func isAutomaticApprovalEvent(req *kernel.ApprovalPayload) bool {
	if req == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(req.DecisionSource), "auto-review") ||
		strings.TrimSpace(string(req.ReviewStatus)) != "" ||
		strings.TrimSpace(req.ReviewID) != ""
}

func isApprovalGatewayEvent(kind kernel.EventKind) bool {
	return kind == kernel.EventKindApprovalRequested || kind == kernel.EventKindApprovalReview
}

func automaticApprovalReviewDisplayText(req *kernel.ApprovalPayload) string {
	if req == nil {
		return ""
	}
	switch req.ReviewStatus {
	case kernel.ApprovalReviewStatusApproved, kernel.ApprovalReviewStatusDenied, kernel.ApprovalReviewStatusTimedOut, kernel.ApprovalReviewStatusFailed:
		return firstNonEmpty(strings.TrimSpace(req.ReviewText), "Automatic approval review "+strings.TrimSpace(string(req.ReviewStatus)))
	default:
		if text := strings.TrimSpace(req.ReviewText); text != "" {
			return text
		}
		return ""
	}
}

func awaitApprovalPrompt(ctx context.Context, turn tuidriver.Turn, req *kernel.ApprovalPayload, responses <-chan PromptResponse, send func(tea.Msg)) {
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
	if err := turn.Submit(ctx, kernel.SubmitRequest{
		Kind:     kernel.SubmissionKindApproval,
		Approval: &decision,
	}); err != nil {
		sendNotice(send, fmt.Sprintf("approval submit failed: %v", err))
	}
}

func approvalDecisionFromPrompt(req *kernel.ApprovalPayload, response PromptResponse) kernel.ApprovalDecision {
	selected := strings.TrimSpace(response.Line)
	if response.Err != nil || selected == "" {
		return rejectionApprovalDecision(req)
	}
	if req != nil {
		for _, opt := range req.Options {
			if strings.TrimSpace(opt.ID) != selected {
				continue
			}
			return kernel.ApprovalDecision{
				Outcome:  string(kernel.ApprovalStatusSelected),
				OptionID: selected,
				Approved: approvalOptionAllows(opt.Kind, opt.Name, opt.ID),
			}
		}
	}
	switch strings.ToLower(selected) {
	case "approve", "allow", "yes", "y":
		return kernel.ApprovalDecision{Outcome: string(kernel.ApprovalStatusApproved), Approved: true}
	default:
		return rejectionApprovalDecision(req)
	}
}

func rejectionApprovalDecision(req *kernel.ApprovalPayload) kernel.ApprovalDecision {
	if req != nil {
		for _, opt := range req.Options {
			if approvalOptionAllows(opt.Kind, opt.Name, opt.ID) {
				continue
			}
			return kernel.ApprovalDecision{
				Outcome:  string(kernel.ApprovalStatusSelected),
				OptionID: strings.TrimSpace(opt.ID),
				Approved: false,
			}
		}
	}
	return kernel.ApprovalDecision{Outcome: string(kernel.ApprovalStatusRejected), Approved: false}
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

func splitSlash(text string) (cmd, args string) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "/")
	cmd, args, _ = strings.Cut(text, " ")
	cmd = strings.TrimSpace(strings.ToLower(cmd))
	args = strings.TrimSpace(args)
	return
}

func splitFirst(text string) (first, rest string) {
	first, rest, _ = strings.Cut(strings.TrimSpace(text), " ")
	first = strings.TrimSpace(first)
	rest = strings.TrimSpace(rest)
	return
}

type agentAddArgs struct {
	Target  string
	Install bool
	Custom  *tuidriver.CustomAgentConfig
}

func parseAgentAddArgs(args string) (agentAddArgs, bool) {
	fields := strings.Fields(args)
	var out agentAddArgs
	if len(fields) > 0 && strings.EqualFold(fields[0], "custom") {
		if len(fields) < 4 {
			return agentAddArgs{}, false
		}
		name := strings.TrimSpace(fields[1])
		if name == "" || strings.HasPrefix(name, "-") {
			return agentAddArgs{}, false
		}
		delim := -1
		for i := 2; i < len(fields); i++ {
			if fields[i] == "--" {
				delim = i
				break
			}
		}
		if delim < 0 || delim+1 >= len(fields) {
			return agentAddArgs{}, false
		}
		command := strings.TrimSpace(fields[delim+1])
		if command == "" {
			return agentAddArgs{}, false
		}
		return agentAddArgs{
			Target: name,
			Custom: &tuidriver.CustomAgentConfig{
				Name:    name,
				Command: command,
				Args:    append([]string(nil), fields[delim+2:]...),
			},
		}, true
	}
	for _, field := range fields {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "--install", "-i":
			out.Install = true
		default:
			if strings.HasPrefix(field, "-") {
				return agentAddArgs{}, false
			}
			if out.Target != "" {
				return agentAddArgs{}, false
			}
			out.Target = strings.TrimSpace(field)
		}
	}
	return out, true
}

func parseConnectArgs(args string) tuidriver.ConnectConfig {
	parts := strings.Fields(args)
	cfg := tuidriver.ConnectConfig{}
	if len(parts) >= 1 {
		cfg.Provider = parts[0]
	}
	if len(parts) >= 2 {
		cfg.Model = parts[1]
	}
	if len(parts) >= 3 {
		cfg.BaseURL = dashAsEmpty(parts[2])
	}
	if len(parts) >= 4 {
		if timeout, err := strconv.Atoi(dashAsEmpty(parts[3])); err == nil {
			cfg.TimeoutSeconds = timeout
		}
	}
	if len(parts) >= 5 {
		secret := dashAsEmpty(parts[4])
		if strings.HasPrefix(strings.ToLower(secret), "env:") {
			cfg.TokenEnv = strings.TrimSpace(secret[len("env:"):])
		} else if strings.HasPrefix(secret, "$") {
			cfg.TokenEnv = strings.TrimSpace(strings.TrimPrefix(secret, "$"))
		} else {
			cfg.APIKey = secret
		}
	}
	if len(parts) >= 6 {
		if contextWindow, err := strconv.Atoi(dashAsEmpty(parts[5])); err == nil {
			cfg.ContextWindowTokens = contextWindow
		}
	}
	if len(parts) >= 7 {
		if maxOutput, err := strconv.Atoi(dashAsEmpty(parts[6])); err == nil {
			cfg.MaxOutputTokens = maxOutput
		}
	}
	if len(parts) >= 8 {
		cfg.ReasoningLevels = parseReasoningLevels(parts[7])
	}
	if len(parts) == 4 && cfg.TimeoutSeconds == 0 && cfg.APIKey == "" && cfg.TokenEnv == "" {
		cfg.TokenEnv = dashAsEmpty(parts[3])
	}
	return cfg
}

func formatStatusSnapshot(status tuidriver.StatusSnapshot) string {
	model := firstNonEmpty(strings.TrimSpace(status.Model), strings.TrimSpace(status.ModelName), deriveModelNameFromAlias(status.Model), "not configured")
	provider := firstNonEmpty(strings.TrimSpace(status.Provider), deriveProviderFromAlias(status.Model), "not configured")
	sandbox := firstNonEmpty(strings.TrimSpace(status.SandboxResolvedBackend), strings.TrimSpace(status.SandboxType), "auto")
	route := strings.TrimSpace(status.Route)
	if route != "" && route != "-" {
		sandbox += " via " + route
	}
	lines := []string{"Session"}
	lines = append(lines, fmt.Sprintf("  Session    %s", firstNonEmpty(strings.TrimSpace(status.SessionID), "-")))
	lines = append(lines, fmt.Sprintf("  Provider   %s", provider))
	lines = append(lines, fmt.Sprintf("  Model      %s", model))
	lines = append(lines, fmt.Sprintf("  Mode       %s", firstNonEmpty(strings.TrimSpace(status.ModeLabel), "auto-review")))
	lines = append(lines, fmt.Sprintf("  Sandbox    %s", sandbox))
	lines = append(lines, fmt.Sprintf("  Workspace  %s", firstNonEmpty(strings.TrimSpace(status.Workspace), "-")))
	lines = append(lines, fmt.Sprintf("  Store      %s", firstNonEmpty(strings.TrimSpace(status.StoreDir), "-")))
	if usage := formatContextUsageStatus(status.TotalTokens, status.ContextWindowTokens); usage != "" {
		lines = append(lines, fmt.Sprintf("  Context    %s", usage))
	}
	if usage := formatSessionTokenUsageStatus(status); usage != "" {
		lines = append(lines, "  Token usage")
		for _, line := range strings.Split(usage, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines = append(lines, "    "+line)
		}
	}
	if status.PermissionGrantCount > 0 {
		network := "no"
		if status.PermissionGrantNetwork {
			network = "yes"
		}
		lines = append(lines, fmt.Sprintf("  Grants     %d approved, read roots %d, write roots %d, network %s", status.PermissionGrantCount, status.PermissionReadRootCount, status.PermissionWriteRootCount, network))
	}
	if status.FallbackReason != "" {
		lines = append(lines, "  Fallback   "+strings.TrimSpace(status.FallbackReason))
	}
	if status.SandboxInstallHint != "" {
		lines = append(lines, "  Install    "+strings.TrimSpace(status.SandboxInstallHint))
	}
	if strings.TrimSpace(status.Model) == "" && strings.TrimSpace(status.Provider) == "" && strings.TrimSpace(status.ModelName) == "" {
		lines = append(lines, "note: Run /connect to configure a provider and model")
	}
	if status.MissingAPIKey {
		lines = append(lines, "warn: API key is missing; reconnect with a key or use env:YOUR_API_KEY")
	}
	if status.HostExecution || status.FullAccessMode {
		lines = append(lines, "warn: Commands may run on the host with reduced sandbox isolation")
	}
	if status.SandboxAutoReviewDisabled {
		lines = append(lines, "warn: Auto-Review is disabled until a sandbox backend is available")
	}
	if strings.TrimSpace(status.FallbackReason) != "" {
		lines = append(lines, "warn: Requested sandbox backend is unavailable and a fallback is in effect")
	}
	return strings.Join(lines, "\n")
}

func formatSessionTokenUsageStatus(status tuidriver.StatusSnapshot) string {
	total := normalizedUsageSnapshot(status.SessionUsageTotal)
	if usageSnapshotZero(total) {
		total = normalizedUsageSnapshot(kernel.UsageSnapshot{
			PromptTokens:      status.SessionInputTokens,
			CachedInputTokens: status.SessionCachedInputTokens,
			CompletionTokens:  status.SessionOutputTokens,
			ReasoningTokens:   status.SessionReasoningTokens,
			TotalTokens:       status.SessionTotalTokens,
		})
	}
	if usageSnapshotZero(total) {
		return ""
	}
	rows := []tokenUsageStatusRow{{scope: "total", usage: total}}
	main := normalizedUsageSnapshot(status.SessionUsageMain)
	subagents := normalizedUsageSnapshot(status.SessionUsageSubagents)
	autoReview := normalizedUsageSnapshot(status.SessionUsageAutoReview)
	if !usageSnapshotZero(main) {
		rows = append(rows, tokenUsageStatusRow{scope: "main", usage: main})
	}
	if !usageSnapshotZero(subagents) {
		rows = append(rows, tokenUsageStatusRow{scope: "sub-agent", usage: subagents})
	}
	if !usageSnapshotZero(autoReview) {
		rows = append(rows, tokenUsageStatusRow{scope: "auto-review", usage: autoReview})
	}
	return formatTokenUsageTable(rows)
}

type tokenUsageStatusRow struct {
	scope string
	usage kernel.UsageSnapshot
}

func formatTokenUsageTable(rows []tokenUsageStatusRow) string {
	if len(rows) == 0 {
		return ""
	}
	table := make([][]string, 0, len(rows)+1)
	table = append(table, []string{"Scope", "Total", "Input", "Cached", "Output", "Reasoning"})
	for _, row := range rows {
		usage := normalizedUsageSnapshot(row.usage)
		table = append(table, []string{
			row.scope,
			formatTokenUsageNumber(usage.TotalTokens),
			formatTokenUsageNumber(usage.PromptTokens),
			formatTokenUsageNumber(usage.CachedInputTokens),
			formatTokenUsageNumber(usage.CompletionTokens),
			formatTokenUsageNumber(usage.ReasoningTokens),
		})
	}
	widths := make([]int, len(table[0]))
	for _, cols := range table {
		for i, col := range cols {
			if len(col) > widths[i] {
				widths[i] = len(col)
			}
		}
	}
	var b strings.Builder
	for rowIndex, cols := range table {
		if rowIndex > 0 {
			b.WriteByte('\n')
		}
		for colIndex, col := range cols {
			if colIndex > 0 {
				b.WriteString("  ")
			}
			if colIndex == 0 {
				b.WriteString(fmt.Sprintf("%-*s", widths[colIndex], col))
				continue
			}
			b.WriteString(fmt.Sprintf("%*s", widths[colIndex], col))
		}
	}
	return b.String()
}

func normalizedUsageSnapshot(usage kernel.UsageSnapshot) kernel.UsageSnapshot {
	if usage.PromptTokens < 0 {
		usage.PromptTokens = 0
	}
	if usage.CachedInputTokens < 0 {
		usage.CachedInputTokens = 0
	}
	if usage.CompletionTokens < 0 {
		usage.CompletionTokens = 0
	}
	if usage.ReasoningTokens < 0 {
		usage.ReasoningTokens = 0
	}
	if usage.TotalTokens < 0 {
		usage.TotalTokens = 0
	}
	if usage.TotalTokens == 0 && (usage.PromptTokens != 0 || usage.CompletionTokens != 0) {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}

func usageSnapshotZero(usage kernel.UsageSnapshot) bool {
	return usage.PromptTokens == 0 &&
		usage.CachedInputTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.TotalTokens == 0
}

func formatTokenUsageNumber(tokens int) string {
	if tokens < 0 {
		tokens = 0
	}
	text := strconv.Itoa(tokens)
	if len(text) <= 3 {
		return text
	}
	var b strings.Builder
	prefix := len(text) % 3
	if prefix == 0 {
		prefix = 3
	}
	b.WriteString(text[:prefix])
	for i := prefix; i < len(text); i += 3 {
		b.WriteByte(',')
		b.WriteString(text[i : i+3])
	}
	return b.String()
}

func agentHelpText() string {
	lines := []string{
		"/agent commands:",
		"  /agent list          list registered ACP agents and current controller",
		"  /agent add NAME      register a built-in ACP agent",
		"  /agent add custom NAME -- COMMAND [ARGS...]",
		"  /agent install NAME  install an external ACP adapter and register it",
		"  /agent use NAME      switch the main controller to a registered ACP agent",
		"  /agent use local     return the main controller to the local kernel",
		"  /agent remove NAME   unregister an ACP agent",
	}
	return strings.Join(lines, "\n")
}

func formatAgentCatalog(agents []tuidriver.AgentCandidate) string {
	if len(agents) == 0 {
		return "no ACP agents are registered\nnext: run /agent add <builtin>"
	}
	lines := []string{"registered ACP agents:"}
	for _, agent := range agents {
		line := "  " + strings.TrimSpace(agent.Name)
		if desc := strings.TrimSpace(agent.Description); desc != "" {
			line += "  " + desc
		}
		lines = append(lines, line)
	}
	lines = append(lines, "next: use /<agent> <prompt> for a child subagent, or /agent use <agent> to switch the main controller")
	return strings.Join(lines, "\n")
}

func formatAgentList(agents []tuidriver.AgentCandidate, status tuidriver.AgentStatusSnapshot) string {
	lines := []string{"agent registry:"}
	lines = append(lines, fmt.Sprintf("  controller: %s", firstNonEmpty(strings.TrimSpace(status.ControllerLabel), strings.TrimSpace(status.ControllerKind), "local")))
	if len(agents) == 0 {
		lines = append(lines, "  registered: none")
		lines = append(lines, "next: run /agent add <builtin>")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "  registered:")
	for _, agent := range agents {
		line := "    " + strings.TrimSpace(agent.Name)
		if desc := strings.TrimSpace(agent.Description); desc != "" {
			line += "  " + desc
		}
		lines = append(lines, line)
	}
	lines = append(lines, "next: /<agent> <prompt> starts a child; /agent use <agent> switches the main controller")
	return strings.Join(lines, "\n")
}

func formatAgentStatusSnapshot(status tuidriver.AgentStatusSnapshot) string {
	controller := firstNonEmpty(strings.TrimSpace(status.ControllerLabel), strings.TrimSpace(status.ControllerKind), "local kernel")
	kind := firstNonEmpty(strings.TrimSpace(status.ControllerKind), "kernel")
	state := "idle"
	if status.HasActiveTurn {
		state = "running"
	}
	lines := []string{"Agent Controller"}
	lines = append(lines, fmt.Sprintf("  Active    %s", controller))
	lines = append(lines, fmt.Sprintf("  Kind      %s", kind))
	lines = append(lines, fmt.Sprintf("  Session   %s", firstNonEmpty(strings.TrimSpace(status.SessionID), "-")))
	lines = append(lines, fmt.Sprintf("  State     %s", state))
	if model := formatAgentControllerModel(status); model != "" {
		lines = append(lines, fmt.Sprintf("  Model     %s", model))
	}
	participants := displayableAgentParticipants(status.Participants)
	if len(participants) == 0 {
		lines = append(lines, "  Side agents  none")
	} else {
		lines = append(lines, "  Side agents")
		for _, participant := range participants {
			lines = append(lines, fmt.Sprintf("    %s  %s  %s", firstNonEmpty(strings.TrimSpace(participant.ID), "-"), firstNonEmpty(strings.TrimSpace(participant.Label), "-"), strings.TrimSpace(participant.Role)))
		}
	}
	delegated := displayableAgentParticipants(status.DelegatedParticipants)
	if len(delegated) == 0 {
		lines = append(lines, "  Delegated tasks  none")
	} else {
		lines = append(lines, "  Delegated tasks")
		for _, participant := range delegated {
			lines = append(lines, fmt.Sprintf("    %s  %s  %s", firstNonEmpty(strings.TrimSpace(participant.ID), "-"), firstNonEmpty(strings.TrimSpace(participant.Label), "-"), strings.TrimSpace(participant.AgentName)))
		}
	}
	if len(status.AvailableAgents) == 0 {
		lines = append(lines, "note: No ACP agents are configured")
	} else if len(participants) == 0 && strings.TrimSpace(status.ControllerKind) == "" {
		lines = append(lines, "note: Run /agent add <builtin> to register an ACP agent")
	} else if len(participants) == 0 {
		lines = append(lines, fmt.Sprintf("note: Run %s <prompt> to start a child subagent", agentPromptCommand(status)))
	}
	return strings.Join(lines, "\n")
}

func formatAgentControllerModel(status tuidriver.AgentStatusSnapshot) string {
	model := strings.TrimSpace(status.ControllerModel)
	effort := strings.TrimSpace(status.ControllerReasoningEffort)
	if model == "" {
		return ""
	}
	if effort == "" || strings.EqualFold(effort, "none") || strings.Contains(model, "[") {
		return model
	}
	return model + " [" + effort + "]"
}

func agentPromptCommand(status tuidriver.AgentStatusSnapshot) string {
	controller := strings.TrimSpace(firstNonEmpty(status.ControllerLabel, status.ControllerKind))
	controller = strings.TrimPrefix(controller, "/")
	if controller == "" || strings.EqualFold(controller, "kernel") || strings.EqualFold(controller, "local kernel") {
		return "/<agent>"
	}
	if strings.ContainsAny(controller, " \t") {
		return "/<agent>"
	}
	return "/" + controller
}

func displayableAgentParticipants(participants []tuidriver.AgentParticipantSnapshot) []tuidriver.AgentParticipantSnapshot {
	if len(participants) == 0 {
		return nil
	}
	return append([]tuidriver.AgentParticipantSnapshot(nil), participants...)
}

func deriveProviderFromAlias(alias string) string {
	left, _, ok := strings.Cut(strings.TrimSpace(alias), "/")
	if !ok {
		return ""
	}
	return strings.TrimSpace(left)
}

func deriveModelNameFromAlias(alias string) string {
	_, right, ok := strings.Cut(strings.TrimSpace(alias), "/")
	if !ok {
		return ""
	}
	return strings.TrimSpace(right)
}

func modeToggleHint(status tuidriver.StatusSnapshot) string {
	label := firstNonEmpty(strings.TrimSpace(status.ModeLabel), strings.TrimSpace(status.SessionMode), "auto-review")
	switch strings.ToLower(strings.TrimSpace(status.SessionMode)) {
	case "manual":
		return "manual approval mode enabled"
	case "auto-review":
		return "auto-review approval mode enabled"
	default:
		return label + " mode enabled"
	}
}

func friendlyCommandError(action string, err error) error {
	if err == nil {
		return nil
	}
	raw := strings.TrimSpace(err.Error())
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "api key is missing"):
		return fmt.Errorf("%s: API key is missing. Use /connect and paste a key, or enter env:YOUR_API_KEY", action)
	case strings.Contains(lower, "base url is invalid"):
		return fmt.Errorf("%s: base URL is invalid. Use a full URL such as https://api.openai.com/v1", action)
	case strings.Contains(lower, "provider is not supported"), strings.Contains(lower, "unknown provider"):
		return fmt.Errorf("%s: provider is not supported. Run /connect and choose one of the listed providers", action)
	case strings.Contains(lower, "provider and model are required"), strings.Contains(lower, "model is required"):
		return fmt.Errorf("%s: provider or model is not configured. Run /connect to add one", action)
	case strings.Contains(lower, "unknown model alias"):
		return fmt.Errorf("%s: model alias was not found. Run /model and choose a configured alias, or use /connect first", action)
	case strings.Contains(lower, "ambiguous model alias"):
		return fmt.Errorf("%s: model alias is ambiguous. Type more of the alias or pick from /model", action)
	case strings.Contains(lower, "agent name is required"), strings.Contains(lower, "agent ") && (strings.Contains(lower, " is not configured") || strings.Contains(lower, " not found")):
		return fmt.Errorf("%s: agent was not found. Run /agent add <builtin> first, then /agent list to inspect registered agents", action)
	case strings.Contains(lower, "agent ") && strings.Contains(lower, " is ambiguous"):
		return fmt.Errorf("%s: agent name is ambiguous. Type more of the agent name or run /agent list", action)
	case strings.Contains(lower, "subagent handle") && strings.Contains(lower, "not found"):
		return fmt.Errorf("%s: handle was not found. Use @handle only for side subagents created by /<agent>", action)
	case strings.Contains(lower, "participant id is required"), strings.Contains(lower, "participant ") && strings.Contains(lower, " is not attached"):
		return fmt.Errorf("%s: participant was not found", action)
	case strings.Contains(lower, "participant ") && strings.Contains(lower, " is ambiguous"):
		return fmt.Errorf("%s: participant target is ambiguous", action)
	case strings.Contains(lower, "control plane is not available"), strings.Contains(lower, "acp controller backend is not configured"):
		return fmt.Errorf("%s: ACP control plane is not configured for this stack. Check app assembly agent config before using /agent", action)
	case strings.Contains(lower, "unknown sandbox backend"), strings.Contains(lower, "unsupported by"):
		return fmt.Errorf("%s: sandbox backend is unavailable on this machine. Run /sandbox to inspect available backends", action)
	case strings.Contains(lower, "session not found"):
		return fmt.Errorf("%s: session could not be loaded. Run /resume to inspect available sessions", action)
	case strings.Contains(lower, "active turn"):
		return fmt.Errorf("%s: another turn is still running. Wait for it to finish or interrupt it before reconfiguring", action)
	default:
		return fmt.Errorf("%s: %w", action, err)
	}
}

func dashAsEmpty(value string) string {
	value = strings.TrimSpace(value)
	if value == "-" {
		return ""
	}
	return value
}

func parseReasoningLevels(raw string) []string {
	raw = dashAsEmpty(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(part))
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func convertAttachments(items []Attachment) []tuidriver.Attachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]tuidriver.Attachment, len(items))
	for i, item := range items {
		out[i] = tuidriver.Attachment{
			Name:   item.Name,
			Offset: item.Offset,
		}
	}
	return out
}

// Ensure gateway import is used.
var _ kernel.EventEnvelope
