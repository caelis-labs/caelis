package tuiapp

// driver_bridge.go bridges the TUI driver contract into the legacy Config
// callback fields. This is the key migration adapter.

import (
	"context"
	"encoding/json"
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
	return fmt.Sprintf("ctx %s / %s · %d%%", formatCompactTokenCount(totalTokens), formatCompactTokenCount(contextWindow), percent)
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
			lines = append(lines, fmt.Sprintf("    %s  %s  %s", firstNonEmpty(strings.TrimSpace(participant.ID), "-"), agentParticipantDisplayLabel(participant), strings.TrimSpace(participant.Role)))
		}
	}
	delegated := displayableAgentParticipants(status.DelegatedParticipants)
	if len(delegated) == 0 {
		lines = append(lines, "  Delegated tasks  none")
	} else {
		lines = append(lines, "  Delegated tasks")
		for _, participant := range delegated {
			lines = append(lines, fmt.Sprintf("    %s  %s  %s", firstNonEmpty(strings.TrimSpace(participant.ID), "-"), agentParticipantDisplayLabel(participant), strings.TrimSpace(participant.AgentName)))
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

func agentParticipantDisplayLabel(participant tuidriver.AgentParticipantSnapshot) string {
	label := strings.TrimSpace(participant.Label)
	agent := strings.TrimSpace(participant.AgentName)
	if label == "" {
		if agent != "" {
			return agent
		}
		return "-"
	}
	if agent == "" {
		return label
	}
	return label + "(" + agent + ")"
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
