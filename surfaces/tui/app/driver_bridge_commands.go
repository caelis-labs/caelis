package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/kernel"
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
	case "settings":
		return slashSharedCommandWithContext(ctx, driver, send, strings.TrimSpace("/settings "+args), sharedCommandOptions{})
	case "task":
		return slashSharedCommandWithContext(ctx, driver, send, strings.TrimSpace("/task "+args), sharedCommandOptions{})
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
	if !isRegisteredAgentCommandWithContext(ctx, driver, agent) {
		sendNotice(send, fmt.Sprintf("unknown command: /%s\nrun /help to see supported commands", agent))
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	if prompt == "" && len(attachments) == 0 {
		sendNotice(send, fmt.Sprintf("usage: /%s <prompt>", agent))
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	input := "/" + agent
	if prompt != "" {
		input += " " + prompt
	}
	return slashSharedCommandWithContext(ctx, driver, send, input, sharedCommandOptions{
		Attachments:     attachments,
		KeepTurnDivider: true,
	})
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
	sub, _ := splitFirst(strings.TrimSpace(args))
	switch sub {
	case "", "help":
		sendNotice(send, agentHelpText())
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	opts := sharedCommandOptions{}
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "add", "register", "install", "update", "remove", "rm", "delete", "del", "use":
		opts.RefreshStatus = true
		opts.RefreshCommands = true
	}
	return slashSharedCommandWithContext(ctx, driver, send, strings.TrimSpace("/agent "+args), opts)
}

func slashNew(driver tuidriver.Driver, send func(tea.Msg)) TaskResultMsg {
	return slashNewWithContext(context.Background(), driver, send)
}

func slashNewWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg)) TaskResultMsg {
	return slashSharedCommandWithContext(ctx, driver, send, "/new", sharedCommandOptions{
		ClearHistory:  true,
		RefreshStatus: true,
	})
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
	case kernel.EventKindPlanUpdate:
		return event.Plan != nil && len(event.Plan.Entries) > 0
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
	return slashSharedCommandWithContext(ctx, driver, send, strings.TrimSpace("/doctor "+args), sharedCommandOptions{})
}

func slashConnect(driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	return slashConnectWithContext(context.Background(), driver, send, args)
}

func slashConnectWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	args = strings.TrimSpace(args)
	if args == "" {
		sendNotice(send, "usage: /connect\nrun /connect to open the guided setup wizard")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	provider, _ := splitFirst(args)
	if strings.EqualFold(strings.TrimSpace(provider), "codefree") {
		sendNotice(send, "opening CodeFree OAuth in your browser and waiting for authentication...")
	}
	return slashSharedCommandWithContext(ctx, driver, send, strings.TrimSpace("/connect "+args), sharedCommandOptions{RefreshStatus: true})
}

func slashModel(driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	return slashModelWithContext(context.Background(), driver, send, args)
}

func slashModelWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	return slashSharedCommandWithContext(ctx, driver, send, strings.TrimSpace("/model "+args), sharedCommandOptions{RefreshStatus: true})
}

func slashApprovalWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	return slashSharedCommandWithContext(ctx, driver, send, strings.TrimSpace("/approval "+args), sharedCommandOptions{RefreshStatus: true})
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
	return slashSharedCommandWithContext(ctx, driver, send, "/compact", sharedCommandOptions{})
}
