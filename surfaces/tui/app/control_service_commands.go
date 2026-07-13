package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	controlcommands "github.com/caelis-labs/caelis/ports/controlcommand"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/surfaces/transcript"
)

func dispatchSlashCommand(service control.Service, sender *ProgramSender, text string) TaskResultMsg {
	return dispatchSlashCommandWithContext(context.Background(), service, sender, text, nil)
}

func dispatchSlashCommandWithContext(ctx context.Context, service control.Service, sender *ProgramSender, text string, attachments []Attachment) TaskResultMsg {
	return dispatchSlashCommandWithContextResult(ctx, service, sender, text, attachments).completion
}

func dispatchSlashCommandWithContextResult(ctx context.Context, service control.Service, sender *ProgramSender, text string, attachments []Attachment) executeLineResult {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	cmd, args, _, _ := controlprompt.ParseSlash(text)
	return dispatchTUIPrivateSlashCommandWithContext(ctx, service, sender, cmd, args)
}

func dispatchTUIPrivateSlashCommandWithContext(ctx context.Context, service control.Service, sender *ProgramSender, cmd string, args string) executeLineResult {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()

	switch cmd {
	case "agent":
		return executeLineResult{completion: slashAgentPrivateWithContext(ctx, service, send, args)}
	case "plugin":
		return executeLineResult{completion: slashPluginWithContext(ctx, service, send, args)}
	case "connect":
		return executeLineResult{completion: slashConnectWithContext(ctx, service, send, args)}
	case "exit", "quit":
		return executeLineResult{completion: TaskResultMsg{ExitNow: true}}
	default:
		sendNotice(send, fmt.Sprintf("unknown TUI command: /%s", cmd))
		return executeLineResult{completion: TaskResultMsg{SuppressTurnDivider: true}}
	}
}

func executeTUIPrivateSlashCommandWithContext(ctx context.Context, service control.Service, sender *ProgramSender, cmd string, args string) (executeLineResult, bool) {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if cmd == "" {
		return executeLineResult{}, false
	}
	if strings.EqualFold(cmd, "agent") && isTUIPrivateAgentSlash(args) {
		if _, activeACP := control.ActiveACPStatus(ctx, service); activeACP {
			if !controlcommands.IsLocalDuringACP(cmd) {
				return executeLineResult{}, false
			}
		}
		return dispatchTUIPrivateSlashCommandWithContext(ctx, service, sender, cmd, args), true
	}
	if controlcommands.IsSharedKnown(cmd) || !controlcommands.IsKnown(cmd) {
		return executeLineResult{}, false
	}
	if _, activeACP := control.ActiveACPStatus(ctx, service); activeACP {
		if !controlcommands.IsLocalDuringACP(cmd) {
			return executeLineResult{}, false
		}
	}
	return dispatchTUIPrivateSlashCommandWithContext(ctx, service, sender, cmd, args), true
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

func runSubagentTurn(ctx context.Context, sender *ProgramSender, turn control.Turn) executeLineResult {
	if turn == nil {
		return executeLineResult{completion: TaskResultMsg{SuppressTurnDivider: true}}
	}
	defer turn.Close()
	if sender != nil && sender.sendFunc() != nil {
		return forwardTurnEventStream(ctx, turn, sender)
	}
	for range turn.Events() {
	}
	return executeLineResult{completion: TaskResultMsg{}}
}

func projectResumeReplayEvents(events []eventstream.Envelope) []TranscriptEvent {
	return transcript.ProjectReplayEvents(events, tuiTranscriptProjector{})
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
	sendNotice(send, fmt.Sprintf("connected: %s", status.ModelStatus.Display))
	sendStatusUpdate(send, status)
	return TaskResultMsg{SuppressTurnDivider: true}
}
