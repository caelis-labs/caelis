package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
	tuicommands "github.com/OnslaughtSnail/caelis/surfaces/tui/commands"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

func dispatchSlashCommand(driver tuidriver.Driver, sender *ProgramSender, text string) TaskResultMsg {
	return dispatchSlashCommandWithContext(context.Background(), driver, sender, text, nil)
}

func dispatchSlashCommandWithContext(ctx context.Context, driver tuidriver.Driver, sender *ProgramSender, text string, attachments []Attachment) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	cmd, args, argsStart := splitSlashWithPromptSpan(text)
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
	case "doctor":
		return slashDoctorWithContext(ctx, driver, send, args)
	case "connect":
		return slashConnectWithContext(ctx, driver, send, args)
	case "model":
		return slashModelWithContext(ctx, driver, send, args)
	case "approval":
		return slashApprovalWithContext(ctx, driver, send, args)
	case "compact":
		return slashCompactWithContext(ctx, driver, send, args)
	case "exit", "quit":
		return TaskResultMsg{ExitNow: true}
	default:
		return slashDynamicAgentWithContext(ctx, driver, sender, cmd, args, attachmentsForPromptRange(attachments, argsStart, len([]rune(strings.TrimSpace(text)))))
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
	return tuicommands.IsLocalDuringACP(cmd)
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
	if tuicommands.IsKnown(cmd) {
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
	return slashDynamicAgentWithContext(context.Background(), driver, &ProgramSender{Send: send}, agent, prompt, nil)
}

func slashDynamicAgentWithContext(ctx context.Context, driver tuidriver.Driver, sender *ProgramSender, agent string, prompt string, attachments []Attachment) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	agent = strings.ToLower(strings.TrimSpace(agent))
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && len(attachments) == 0 {
		if isRegisteredAgentCommand(driver, agent) {
			sendNotice(send, fmt.Sprintf("usage: /%s <prompt>", agent))
		} else {
			sendNotice(send, fmt.Sprintf("unknown command: /%s\nrun /help to see supported commands", agent))
		}
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	turn, err := driver.StartAgentSubagent(ctx, agent, prompt, convertAttachments(attachments))
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
	return dispatchMentionCommandWithContext(context.Background(), driver, sender, text, nil)
}

func dispatchMentionCommandWithContext(ctx context.Context, driver tuidriver.Driver, sender *ProgramSender, text string, attachments []Attachment) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	handle, prompt, promptStart := splitFirstWithPromptSpan(strings.TrimSpace(text))
	handle = strings.TrimPrefix(strings.TrimSpace(handle), "@")
	attachments = attachmentsForPromptRange(attachments, promptStart, len([]rune(strings.TrimSpace(text))))
	if handle == "" || (strings.TrimSpace(prompt) == "" && len(attachments) == 0) {
		sendNotice(send, "usage: @handle <prompt>")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	turn, err := driver.ContinueSubagent(ctx, handle, prompt, convertAttachments(attachments))
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
		sendNotice(send, "usage: /agent list | add <builtin> | install/update <adapter> | use <agent|local> | remove <agent>")
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
	case "install", "update":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, fmt.Sprintf("usage: /agent %s <adapter>", sub))
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		command := agentInstallCommandForDisplay(ctx, driver, sub, target)
		callID := sendAgentInstallToolCall(send, target, command)
		status, err := driver.AddAgentWithOptions(ctx, target, tuidriver.AgentAddOptions{Install: true})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				sendAgentInstallToolResult(send, callID, command, kernel.ToolStatusInterrupted, false, agentInstallErrorOutput(err))
				return TaskResultMsg{Interrupted: true, SuppressTurnDivider: true}
			}
			sendAgentInstallToolResult(send, callID, command, kernel.ToolStatusFailed, true, agentInstallErrorOutput(err))
			return TaskResultMsg{Err: friendlyCommandError("agent "+sub, err)}
		}
		sendAgentInstallToolResult(send, callID, command, kernel.ToolStatusCompleted, false, "")
		if sub == "update" {
			sendNotice(send, fmt.Sprintf("agent updated and registered: %s", target))
		} else {
			sendNotice(send, fmt.Sprintf("agent installed and registered: %s", target))
		}
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
		sendNotice(send, "usage: /agent list | add <builtin> | install/update <adapter> | use <agent|local> | remove <agent>")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func agentInstallCommandForDisplay(ctx context.Context, driver tuidriver.Driver, action string, target string) string {
	action = strings.TrimSpace(strings.ToLower(action))
	if action == "" {
		action = "install"
	}
	target = strings.TrimSpace(target)
	if driver != nil {
		if candidates, err := driver.CompleteSlashArg(ctx, "agent "+action, target, 20); err == nil {
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
			ToolName: "RUN_COMMAND",
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
	if renderableTextHasContent(output) {
		if isErr {
			rawOutput["stderr"] = output
		} else {
			rawOutput["stdout"] = output
		}
	}
	contentText := output
	content := []session.ProtocolToolCallContent{}
	if renderableTextHasContent(contentText) {
		content = []session.ProtocolToolCallContent{{
			Type:    "terminal",
			Content: session.ProtocolTextContent(contentText),
		}}
	}
	send(kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindToolResult,
		OccurredAt: time.Now(),
		ToolResult: &kernel.ToolResultPayload{
			CallID:    callID,
			ToolName:  "RUN_COMMAND",
			Status:    status,
			Scope:     kernel.EventScopeMain,
			RawInput:  map[string]any{"command": strings.TrimSpace(command)},
			RawOutput: rawOutput,
			Content:   content,
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
		if transcriptEvents := resumeTranscriptReplayTranscriptEvents(events); len(transcriptEvents) > 0 && send != nil {
			send(TranscriptEventsMsg{Events: transcriptEvents})
		}
	}

	refreshStatusViaSendWithContext(ctx, driver, send)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func resumeTranscriptReplayTranscriptEvents(events []kernel.EventEnvelope) []TranscriptEvent {
	envelopes := resumeTranscriptReplayEvents(events)
	if len(envelopes) == 0 {
		return nil
	}
	out := make([]TranscriptEvent, 0, len(envelopes))
	for _, env := range envelopes {
		projected := ProjectGatewayEventToTranscriptEvents(env.Event)
		if len(projected) == 0 {
			if event, ok := resumeParticipantUserTranscriptEvent(env.Event); ok {
				projected = append(projected, event)
			}
		}
		out = append(out, projected...)
	}
	return out
}

func resumeParticipantUserTranscriptEvent(event kernel.Event) (TranscriptEvent, bool) {
	if event.Kind != kernel.EventKindUserMessage || gatewayEventScope(event) != ACPProjectionParticipant {
		return TranscriptEvent{}, false
	}
	text := strings.TrimSpace(gatewayUserText(event))
	if text == "" {
		return TranscriptEvent{}, false
	}
	label := firstNonEmpty(
		kernel.EventMetaString(event.Meta, "mention"),
		kernel.EventMetaString(event.Meta, "handle"),
	)
	if label != "" && !strings.HasPrefix(label, "@") {
		label = "@" + label
	}
	if event.Origin != nil {
		label = firstNonEmpty(label, event.Origin.ParticipantID, event.Origin.Actor)
	}
	label = firstNonEmpty(label, "side ACP")
	return TranscriptEvent{
		Kind:          TranscriptEventNarrative,
		Scope:         ACPProjectionMain,
		NarrativeKind: TranscriptNarrativeUser,
		Text:          fmt.Sprintf("User to %s: %s", label, text),
		Final:         true,
		OccurredAt:    event.OccurredAt,
	}, true
}

func resumeTranscriptReplayEvents(events []kernel.EventEnvelope) []kernel.EventEnvelope {
	if len(events) == 0 {
		return nil
	}
	out := make([]kernel.EventEnvelope, 0, len(events))
	for _, env := range events {
		if shouldReplayEventInTUIResume(env.Event) {
			out = append(out, env)
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
	return true
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

func slashDoctorWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "":
	case "fix":
		return slashDoctorFixWithContext(ctx, driver, send)
	default:
		sendNotice(send, "usage: /doctor [fix]")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	status, err := driver.Status(ctx)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("doctor", err)}
	}
	sendNotice(send, formatDoctorSnapshot(status))
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashDoctorFixWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg)) TaskResultMsg {
	if driver == nil {
		return TaskResultMsg{Err: friendlyCommandError("doctor fix", fmt.Errorf("driver unavailable"))}
	}
	sendNotice(send, "Windows sandbox repair started. Approve the UAC prompt if shown.")
	status, err := driver.RepairSandbox(ctx)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("doctor fix", err)}
	}
	if sandboxSetupStillRequired(status) {
		sendNotice(send, "Windows sandbox repair still needs attention. Run /doctor for details.")
	} else {
		sendNotice(send, "Windows sandbox repair complete.")
	}
	return TaskResultMsg{SuppressTurnDivider: true}
}

func sandboxSetupStillRequired(status tuidriver.StatusSnapshot) bool {
	global, hasGlobal := status.SandboxSetup.Check("global")
	workspace, hasWorkspace := status.SandboxSetup.Check("workspace")
	return status.SandboxSetupRequired ||
		status.SandboxGlobalSetupRequired ||
		status.SandboxWorkspaceSetupRequired ||
		(hasGlobal && global.Required) ||
		(hasWorkspace && workspace.Required)
}

func formatDoctorSnapshot(status tuidriver.StatusSnapshot) string {
	lines := []string{"doctor:"}
	provider := strings.TrimSpace(firstNonEmpty(status.Provider, status.Model))
	modelName := strings.TrimSpace(firstNonEmpty(status.ModelName, status.Model))
	switch {
	case status.MissingAPIKey:
		lines = append(lines, "  warn provider key missing - run /connect")
	case provider == "" && modelName == "":
		lines = append(lines, "  warn model not configured - run /connect")
	default:
		lines = append(lines, "  ok provider/model: "+joinNonEmpty([]string{provider, modelName}, " / "))
	}
	if storeDir := strings.TrimSpace(status.StoreDir); storeDir != "" {
		lines = append(lines, "  ok session store: "+storeDir)
	} else {
		lines = append(lines, "  warn session store path unavailable")
	}
	if sessionID := strings.TrimSpace(status.SessionID); sessionID != "" {
		lines = append(lines, "  ok session: "+sessionID)
	}
	sandbox := strings.TrimSpace(firstNonEmpty(status.SandboxResolvedBackend, status.SandboxRequestedBackend, status.SandboxType))
	globalSetup, hasGlobalSetup := status.SandboxSetup.Check("global")
	workspaceSetup, hasWorkspaceSetup := status.SandboxSetup.Check("workspace")
	globalSetupRequired := status.SandboxGlobalSetupRequired || (hasGlobalSetup && globalSetup.Required)
	workspaceSetupRequired := status.SandboxWorkspaceSetupRequired || (hasWorkspaceSetup && workspaceSetup.Required)
	switch {
	case status.HostExecution || status.FullAccessMode:
		detail := strings.TrimSpace(firstNonEmpty(status.SecuritySummary, sandbox, "host execution"))
		lines = append(lines, "  warn sandbox: "+detail)
	case globalSetupRequired:
		detail := strings.TrimSpace(firstNonEmpty(status.SandboxSetupError, globalSetup.Error, globalSetup.Reason, status.SandboxGlobalSetupReason, status.SandboxSetupMarkerReason, "global setup required"))
		lines = append(lines, "  warn sandbox global repair pending: "+compactStatusDetail(detail, 180))
		if strings.TrimSpace(firstNonEmpty(status.SandboxSetupError, globalSetup.Error)) != "" {
			lines = append(lines, "  info fix: /doctor fix")
		}
	case workspaceSetupRequired:
		detail := strings.TrimSpace(firstNonEmpty(status.SandboxSetupError, workspaceSetup.Error, workspaceSetup.Reason, status.SandboxWorkspaceSetupReason, "workspace ACL setup required"))
		lines = append(lines, "  warn sandbox workspace repair pending: "+compactStatusDetail(detail, 180))
		if strings.TrimSpace(firstNonEmpty(status.SandboxSetupError, workspaceSetup.Error)) != "" {
			lines = append(lines, "  info fix: /doctor fix")
		}
	case sandbox != "":
		lines = append(lines, "  ok sandbox: "+sandbox)
	default:
		lines = append(lines, "  warn sandbox status unavailable")
	}
	if route := strings.TrimSpace(status.Route); route != "" {
		lines = append(lines, "  ok route: "+route)
	}
	if status.ActiveJobs > 0 || status.Running {
		lines = append(lines, fmt.Sprintf("  info active jobs: %d", status.ActiveJobs))
	}
	return strings.Join(lines, "\n")
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
