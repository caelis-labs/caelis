package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	controlcommands "github.com/OnslaughtSnail/caelis/protocol/acp/control/commands"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func dispatchSlashCommand(service control.Service, sender *ProgramSender, text string) TaskResultMsg {
	return dispatchSlashCommandWithContext(context.Background(), service, sender, text, nil)
}

func dispatchSlashCommandWithContext(ctx context.Context, service control.Service, sender *ProgramSender, text string, attachments []Attachment) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	cmd, args, argsStart := splitSlashWithPromptSpan(text)
	send := sender.sendFunc()

	switch cmd {
	case "help":
		return slashHelpWithContext(ctx, service, send)
	case "agent":
		return slashAgentWithContext(ctx, service, send, args)
	case "plugin":
		return slashPluginWithContext(ctx, service, send, args)
	case "subagent":
		return slashSubagentWithContext(ctx, service, sender, args, argsStart, text, attachments)
	case "new":
		return slashNewWithContext(ctx, service, send)
	case "resume":
		return slashResumeWithContext(ctx, service, send, args)
	case "status":
		return slashStatusWithContext(ctx, service, send)
	case "doctor":
		return slashDoctorWithContext(ctx, service, send, args)
	case "connect":
		return slashConnectWithContext(ctx, service, send, args)
	case "model":
		return slashModelWithContext(ctx, service, send, args)
	case "compact":
		return slashCompactWithContext(ctx, service, send, args)
	case "exit", "quit":
		return TaskResultMsg{ExitNow: true}
	default:
		return slashDynamicAgentWithContext(ctx, service, sender, cmd, args, attachmentsForPromptRange(attachments, argsStart, len([]rune(strings.TrimSpace(text)))))
	}
}

func isDispatchableSlashCommand(service control.Service, text string) bool {
	return isDispatchableSlashCommandWithContext(context.Background(), service, text)
}

func activeACPAgentStatus(ctx context.Context, service control.Service) (control.AgentStatusSnapshot, bool) {
	if service == nil {
		return control.AgentStatusSnapshot{}, false
	}
	status, err := service.AgentStatus(contextOrBackground(ctx))
	if err != nil {
		return control.AgentStatusSnapshot{}, false
	}
	return status, strings.EqualFold(strings.TrimSpace(status.ControllerKind), "acp")
}

func isCoreLocalSlashCommand(cmd string) bool {
	return controlcommands.IsLocalDuringACP(cmd)
}

func controlServiceCanSubmitRunningPrompt(ctx context.Context, service control.Service) bool {
	if service == nil {
		return true
	}
	status, err := service.AgentStatus(contextOrBackground(ctx))
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

func isDispatchableSlashCommandWithContext(ctx context.Context, service control.Service, text string) bool {
	cmd, _ := splitSlash(text)
	if cmd == "" {
		return false
	}
	if _, activeACP := activeACPAgentStatus(ctx, service); activeACP {
		return isCoreLocalSlashCommand(cmd)
	}
	if controlcommands.IsKnown(cmd) {
		return true
	}
	return isRegisteredAgentCommandWithContext(ctx, service, cmd)
}

func slashHelp(send func(tea.Msg)) TaskResultMsg {
	sendNotice(send, defaultHelpText())
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashHelpWithContext(ctx context.Context, service control.Service, send func(tea.Msg)) TaskResultMsg {
	sendNotice(send, helpTextForCommands(appendAgentSlashCommandsWithContext(ctx, service, DefaultCommands())))
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashDynamicAgent(service control.Service, send func(tea.Msg), agent string, prompt string) TaskResultMsg {
	return slashDynamicAgentWithContext(context.Background(), service, &ProgramSender{Send: send}, agent, prompt, nil)
}

func slashDynamicAgentWithContext(ctx context.Context, service control.Service, sender *ProgramSender, agent string, prompt string, attachments []Attachment) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	agent = strings.ToLower(strings.TrimSpace(agent))
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && len(attachments) == 0 {
		if isRegisteredAgentCommand(service, agent) {
			sendNotice(send, fmt.Sprintf("usage: /%s <prompt>", agent))
		} else {
			sendNotice(send, fmt.Sprintf("unknown command: /%s\nrun /help to see supported commands", agent))
		}
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	turn, err := service.StartAgentSubagent(ctx, agent, prompt, convertAttachments(attachments))
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("/"+agent, err)}
	}
	if turn == nil {
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	defer turn.Close()
	if send != nil {
		forwardTurnEventStream(ctx, service, turn, sender)
	} else {
		for range turn.Events() {
		}
	}
	return TaskResultMsg{}
}

func isRegisteredAgentCommand(service control.Service, agent string) bool {
	return isRegisteredAgentCommandWithContext(context.Background(), service, agent)
}

func isRegisteredAgentCommandWithContext(ctx context.Context, service control.Service, agent string) bool {
	ctx = contextOrBackground(ctx)
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return false
	}
	agents, err := service.ListAgents(ctx, 200)
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

func dispatchMentionCommand(service control.Service, sender *ProgramSender, text string) TaskResultMsg {
	return dispatchMentionCommandWithContext(context.Background(), service, sender, text, nil)
}

func dispatchMentionCommandWithContext(ctx context.Context, service control.Service, sender *ProgramSender, text string, attachments []Attachment) TaskResultMsg {
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
	turn, err := service.ContinueSubagent(ctx, handle, prompt, convertAttachments(attachments))
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("@"+handle, err)}
	}
	if turn == nil {
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	defer turn.Close()
	if send != nil {
		forwardTurnEventStream(ctx, service, turn, sender)
	} else {
		for range turn.Events() {
		}
	}
	return TaskResultMsg{}
}

func slashAgent(service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	return slashAgentWithContext(context.Background(), service, send, args)
}

func slashAgentWithContext(ctx context.Context, service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	sub, rest := splitFirst(strings.TrimSpace(args))
	switch sub {
	case "", "help":
		sendNotice(send, agentHelpText())
		return TaskResultMsg{SuppressTurnDivider: true}
	case "list":
		agents, err := service.ListAgents(ctx, 20)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("agent list", err)}
		}
		status, _ := service.AgentStatus(ctx)
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
		_, err := service.AddAgentWithOptions(ctx, addArgs.Target, control.AgentAddOptions{
			Install: addArgs.Install,
			Custom:  addArgs.Custom,
		})
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("agent add", err)}
		}
		sendNotice(send, formatAgentReadyNotice(addArgs.Target))
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	case "install":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /agent install <adapter>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		command := agentInstallCommandForDisplay(ctx, service, target)
		callID := sendAgentInstallToolCall(send, target, command)
		_, err := service.AddAgentWithOptions(ctx, target, control.AgentAddOptions{Install: true})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				sendAgentInstallToolResult(send, callID, command, transcriptToolStatusInterrupted, false, agentInstallErrorOutput(err))
				return TaskResultMsg{Interrupted: true, SuppressTurnDivider: true}
			}
			sendAgentInstallToolResult(send, callID, command, schema.ToolStatusFailed, true, agentInstallErrorOutput(err))
			return TaskResultMsg{Err: friendlyCommandError("agent install", err)}
		}
		sendAgentInstallToolResult(send, callID, command, schema.ToolStatusCompleted, false, "")
		sendNotice(send, formatAgentReadyNotice(target))
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	case "remove":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /agent remove <agent>\nrun /agent list to inspect registered agents")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		_, err := service.RemoveAgent(ctx, target)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("agent remove", err)}
		}
		sendNotice(send, formatAgentRemovedNotice(target))
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	case "use":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /agent use <agent|local>\nrun /agent list for registered agents")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		status, err := service.HandoffAgent(ctx, target)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("agent use", err)}
		}
		sendNotice(send, formatAgentUseNotice(target, status))
		if current, err := service.Status(ctx); err == nil {
			sendStatusUpdate(send, current)
		}
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, "usage: /agent list | add <builtin> | install <adapter> | use <agent|local> | remove <agent>")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func agentInstallCommandForDisplay(ctx context.Context, service control.Service, target string) string {
	target = strings.TrimSpace(target)
	if service != nil {
		if candidates, err := service.CompleteSlashArg(ctx, "agent install", target, 20); err == nil {
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
	send(eventstream.Envelope{
		Kind:       eventstream.KindSessionUpdate,
		OccurredAt: time.Now(),
		Scope:      eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    callID,
			Title:         "RUN_COMMAND",
			Kind:          "RUN_COMMAND",
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": strings.TrimSpace(command)},
			Meta:          agentInstallToolMeta(),
		},
	})
	return callID
}

func sendAgentInstallToolResult(send func(tea.Msg), callID string, command string, status string, isErr bool, output string) {
	if send == nil || strings.TrimSpace(callID) == "" {
		return
	}
	rawOutput := map[string]any{
		"running": false,
		"state":   strings.TrimSpace(status),
	}
	if renderableTextHasContent(output) {
		if isErr {
			rawOutput["stderr"] = output
		} else {
			rawOutput["stdout"] = output
		}
	}
	contentText := output
	content := []schema.ToolCallContent{}
	if renderableTextHasContent(contentText) {
		content = []schema.ToolCallContent{{
			Type:    "terminal",
			Content: schema.TextContent{Type: "text", Text: contentText},
		}}
	}
	title := "RUN_COMMAND"
	kind := "RUN_COMMAND"
	updateStatus := agentInstallACPStatus(status)
	send(eventstream.Envelope{
		Kind:       eventstream.KindSessionUpdate,
		OccurredAt: time.Now(),
		Scope:      eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    callID,
			Title:         &title,
			Kind:          &kind,
			Status:        &updateStatus,
			RawInput:      map[string]any{"command": strings.TrimSpace(command)},
			RawOutput:     rawOutput,
			Content:       content,
			Meta:          agentInstallToolMeta(),
		},
	})
}

func agentInstallACPStatus(status string) string {
	switch status {
	case transcriptToolStatusStarted, transcriptToolStatusRunning:
		return schema.ToolStatusInProgress
	case schema.ToolStatusCompleted:
		return schema.ToolStatusCompleted
	case schema.ToolStatusFailed:
		return schema.ToolStatusFailed
	default:
		return strings.TrimSpace(status)
	}
}

func agentInstallToolMeta() map[string]any {
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"tool": map[string]any{"name": "RUN_COMMAND"},
			},
		},
	}
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

func slashNew(service control.Service, send func(tea.Msg)) TaskResultMsg {
	return slashNewWithContext(context.Background(), service, send)
}

func slashNewWithContext(ctx context.Context, service control.Service, send func(tea.Msg)) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	session, err := service.NewSession(ctx)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("new session", err)}
	}
	if send != nil {
		send(ClearHistoryMsg{})
	}
	sendNotice(send, fmt.Sprintf("new session: %s", session.SessionID))
	refreshStatusViaSendWithContext(ctx, service, send)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashResume(service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	return slashResumeWithContext(context.Background(), service, send, args)
}

func slashResumeWithContext(ctx context.Context, service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	sessionID := strings.TrimSpace(args)
	if sessionID == "" {
		// List available sessions.
		candidates, err := service.ListSessions(ctx, 10)
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
	if _, err := service.ResumeSession(ctx, sessionID); err != nil {
		return TaskResultMsg{Err: friendlyCommandError("resume session", err)}
	}
	if send != nil {
		send(ClearHistoryMsg{})
	}

	// Replay historical events into transcript.
	events, err := service.ReplayEvents(ctx)
	if err != nil {
		sendNotice(send, fmt.Sprintf("warning: replay failed: %v", err))
	} else if len(events) > 0 {
		if transcriptEvents := resumeTranscriptReplayTranscriptEvents(events); len(transcriptEvents) > 0 && send != nil {
			send(TranscriptEventsMsg{Events: transcriptEvents})
		}
	}

	refreshStatusViaSendWithContext(ctx, service, send)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func resumeTranscriptReplayTranscriptEvents(events []eventstream.Envelope) []TranscriptEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]TranscriptEvent, 0, len(events))
	for _, env := range events {
		projected := replayableACPTranscriptEvents(env)
		if len(projected) == 0 {
			if event, ok := resumeParticipantUserACPTranscriptEvent(env); ok {
				projected = append(projected, event)
			}
		}
		out = append(out, projected...)
	}
	return out
}

func replayableACPTranscriptEvents(env eventstream.Envelope) []TranscriptEvent {
	if env.Kind != eventstream.KindSessionUpdate {
		return nil
	}
	update, ok := env.Update.(schema.ContentChunk)
	if !ok {
		return nil
	}
	projected := ProjectACPEventToTranscriptEvents(env)
	if len(projected) == 0 {
		return nil
	}
	switch strings.TrimSpace(update.SessionUpdate) {
	case schema.UpdateUserMessage:
		return projected
	case schema.UpdateAgentMessage, schema.UpdateAgentThought:
		if !env.Final {
			return nil
		}
		return projected
	default:
		return nil
	}
}

func resumeParticipantUserACPTranscriptEvent(env eventstream.Envelope) (TranscriptEvent, bool) {
	if env.Kind != eventstream.KindSessionUpdate || env.Scope != eventstream.ScopeParticipant {
		return TranscriptEvent{}, false
	}
	update, ok := env.Update.(schema.ContentChunk)
	if !ok || strings.TrimSpace(update.SessionUpdate) != schema.UpdateUserMessage {
		return TranscriptEvent{}, false
	}
	text := strings.TrimSpace(protocolTextContent(update.Content))
	if text == "" {
		return TranscriptEvent{}, false
	}
	label := firstNonEmpty(
		metaString(env.Meta, "mention"),
		metaString(env.Meta, "handle"),
	)
	if label != "" && !strings.HasPrefix(label, "@") {
		label = "@" + label
	}
	label = firstNonEmpty(label, env.ParticipantID, env.Actor, env.ScopeID)
	label = firstNonEmpty(label, "side ACP")
	return TranscriptEvent{
		Kind:          TranscriptEventNarrative,
		Scope:         ACPProjectionMain,
		NarrativeKind: TranscriptNarrativeUser,
		Text:          fmt.Sprintf("User to %s: %s", label, text),
		Final:         true,
		OccurredAt:    env.OccurredAt,
	}, true
}

func slashStatus(service control.Service, send func(tea.Msg)) TaskResultMsg {
	return slashStatusWithContext(context.Background(), service, send)
}

func slashStatusWithContext(ctx context.Context, service control.Service, send func(tea.Msg)) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	status, err := service.Status(ctx)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("status", err)}
	}
	sendNotice(send, formatStatusSnapshot(status))
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashDoctorWithContext(ctx context.Context, service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "":
	case "fix":
		return slashDoctorFixWithContext(ctx, service, send)
	default:
		sendNotice(send, "usage: /doctor [fix]")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	status, err := service.Status(ctx)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("doctor", err)}
	}
	sendNotice(send, formatDoctorSnapshot(status))
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashDoctorFixWithContext(ctx context.Context, service control.Service, send func(tea.Msg)) TaskResultMsg {
	if service == nil {
		return TaskResultMsg{Err: friendlyCommandError("doctor fix", fmt.Errorf("service unavailable"))}
	}
	sendNotice(send, "Windows sandbox repair started. Approve the UAC prompt if shown.")
	status, err := service.RepairSandbox(ctx)
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

func sandboxSetupStillRequired(status control.StatusSnapshot) bool {
	global, hasGlobal := status.SandboxSetup.Check("global")
	workspace, hasWorkspace := status.SandboxSetup.Check("workspace")
	return status.SandboxSetupRequired ||
		status.SandboxGlobalSetupRequired ||
		status.SandboxWorkspaceSetupRequired ||
		(hasGlobal && global.Required) ||
		(hasWorkspace && workspace.Required)
}

func formatDoctorSnapshot(status control.StatusSnapshot) string {
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

func slashConnect(service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	return slashConnectWithContext(context.Background(), service, send, args)
}

func slashConnectWithContext(ctx context.Context, service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	cfg := parseConnectArgs(args)
	if cfg.Provider == "" || cfg.Model == "" {
		sendNotice(send, "usage: /connect\nrun /connect to open the guided setup wizard")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Provider), "codefree") {
		sendNotice(send, "opening CodeFree OAuth in your browser and waiting for authentication...")
	}
	status, err := service.Connect(ctx, cfg)
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError("connect", err)}
	}
	sendNotice(send, fmt.Sprintf("connected: %s", status.Model))
	sendStatusUpdate(send, status)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashModel(service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	return slashModelWithContext(context.Background(), service, send, args)
}

func slashModelWithContext(ctx context.Context, service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	sub, rest := splitFirst(strings.TrimSpace(args))
	_, activeACP := activeACPAgentStatus(ctx, service)
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
		status, err := service.UseModel(ctx, alias, reasoning)
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
		if err := service.DeleteModel(ctx, alias); err != nil {
			return TaskResultMsg{Err: friendlyCommandError("model delete", err)}
		}
		sendNotice(send, fmt.Sprintf("model deleted: %s", alias))
		refreshStatusViaSendWithContext(ctx, service, send)
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

func slashCompact(service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	return slashCompactWithContext(context.Background(), service, send, args)
}

func slashCompactWithContext(ctx context.Context, service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if strings.TrimSpace(args) != "" {
		sendNotice(send, "usage: /compact")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	if err := service.Compact(ctx); err != nil {
		return TaskResultMsg{Err: friendlyCommandError("compact", err)}
	}
	sendNotice(send, "compaction completed")
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashPluginWithContext(ctx context.Context, service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	sub, rest := splitFirst(strings.TrimSpace(args))
	switch sub {
	case "":
		sendNotice(send, pluginUsageText())
		return TaskResultMsg{SuppressTurnDivider: true}
	case "manage":
		plugins, err := service.ListPlugins(ctx)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("plugin manage", err)}
		}
		if len(plugins) == 0 {
			sendNotice(send, "no installed plugins\nnext: /plugin install <plugin@marketplace|path>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		sendPluginManagerPrompt(ctx, service, send, plugins)
		return TaskResultMsg{SuppressTurnDivider: true}
	case "install":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /plugin install <plugin@marketplace|directory-path>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		p, err := service.InstallPlugin(ctx, target)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("plugin install", err)}
		}
		sendNotice(send, fmt.Sprintf("installed plugin %s successfully\n\n%s", p.ID, formatPluginDetail(p)))
		return TaskResultMsg{SuppressTurnDivider: true}
	case "rm":
		target := strings.TrimSpace(rest)
		if target == "" {
			sendNotice(send, "usage: /plugin rm <plugin-id>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		err := service.RemovePlugin(ctx, target)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("plugin rm", err)}
		}
		sendNotice(send, fmt.Sprintf("removed plugin %s successfully", target))
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, pluginUsageText())
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func sendPluginManagerPrompt(ctx context.Context, service control.Service, send func(tea.Msg), plugins []control.PluginSnapshot) {
	if send == nil || service == nil || len(plugins) == 0 {
		return
	}
	choices := make([]PromptChoice, 0, len(plugins))
	selected := make([]string, 0, len(plugins))
	for _, p := range plugins {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			continue
		}
		status := p.Status
		if !p.Enabled {
			status = "disabled"
		}
		detail := strings.TrimSpace(strings.Join([]string{
			strings.TrimSpace(status),
			strings.TrimSpace(p.Name),
			strings.TrimSpace(p.Version),
		}, " "))
		choices = append(choices, PromptChoice{
			Label:  id,
			Value:  id,
			Detail: detail,
		})
		if p.Enabled {
			selected = append(selected, id)
		}
	}
	if len(choices) == 0 {
		return
	}
	responses := make(chan PromptResponse, 1)
	send(PromptRequestMsg{
		Title:               "Manage plugins",
		Prompt:              "Select enabled plugins",
		Choices:             choices,
		SelectedChoices:     selected,
		Filterable:          true,
		MultiSelect:         true,
		AllowEmptySelection: true,
		Response:            responses,
	})
	go awaitPluginManagerSelection(context.WithoutCancel(ctx), service, send, plugins, responses)
}

func awaitPluginManagerSelection(ctx context.Context, service control.Service, send func(tea.Msg), plugins []control.PluginSnapshot, responses <-chan PromptResponse) {
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
	if response.Err != nil {
		return
	}
	selected := map[string]struct{}{}
	for _, id := range strings.Split(response.Line, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			selected[id] = struct{}{}
		}
	}
	var enabled, disabled []string
	for _, p := range plugins {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			continue
		}
		_, wantEnabled := selected[id]
		switch {
		case wantEnabled && !p.Enabled:
			if _, err := service.EnablePlugin(ctx, id); err != nil {
				sendNotice(send, fmt.Sprintf("plugin manager failed enabling %s: %v", id, err))
				return
			}
			enabled = append(enabled, id)
		case !wantEnabled && p.Enabled:
			if _, err := service.DisablePlugin(ctx, id); err != nil {
				sendNotice(send, fmt.Sprintf("plugin manager failed disabling %s: %v", id, err))
				return
			}
			disabled = append(disabled, id)
		}
	}
	if len(enabled) == 0 && len(disabled) == 0 {
		sendNotice(send, "plugin selection unchanged")
		return
	}
	parts := make([]string, 0, 2)
	if len(enabled) > 0 {
		parts = append(parts, "enabled "+strings.Join(enabled, ", "))
	}
	if len(disabled) > 0 {
		parts = append(parts, "disabled "+strings.Join(disabled, ", "))
	}
	sendNotice(send, "plugin selection updated: "+strings.Join(parts, "; "))
}

func pluginUsageText() string {
	return "usage: /plugin install <plugin@marketplace|path> | manage | rm <id>"
}

func formatPluginDetail(p control.PluginSnapshot) string {
	lines := []string{fmt.Sprintf("plugin info: %s", p.ID)}
	statusStr := p.Status
	if !p.Enabled {
		statusStr = "disabled"
	}
	lines = append(lines, fmt.Sprintf("  Name:        %s", p.Name))
	lines = append(lines, fmt.Sprintf("  Version:     %s", p.Version))
	lines = append(lines, fmt.Sprintf("  Status:      %s", statusStr))
	lines = append(lines, fmt.Sprintf("  Root Path:   %s", p.Root))
	if p.Description != "" {
		lines = append(lines, fmt.Sprintf("  Description: %s", p.Description))
	}
	if len(p.Skills) > 0 {
		lines = append(lines, fmt.Sprintf("  Skills:      %s", strings.Join(p.Skills, ", ")))
	}
	if len(p.Hooks) > 0 {
		lines = append(lines, fmt.Sprintf("  Hooks:       %s", strings.Join(p.Hooks, ", ")))
	}
	if len(p.Agents) > 0 {
		lines = append(lines, fmt.Sprintf("  Agents:      %s", strings.Join(p.Agents, ", ")))
	}
	if len(p.MCPServers) > 0 {
		lines = append(lines, "  MCP Servers:")
		for _, m := range p.MCPServers {
			mcpLine := fmt.Sprintf("    - %s (%s)", m.Name, m.Status)
			if len(m.Tools) > 0 {
				mcpLine += fmt.Sprintf(" [tools: %s]", strings.Join(m.Tools, ", "))
			}
			if m.Warning != "" {
				mcpLine += fmt.Sprintf(" (warning: %s)", m.Warning)
			}
			lines = append(lines, mcpLine)
		}
	}
	if p.Warning != "" {
		lines = append(lines, fmt.Sprintf("  Warning:     %s", p.Warning))
	}
	return strings.Join(lines, "\n")
}
