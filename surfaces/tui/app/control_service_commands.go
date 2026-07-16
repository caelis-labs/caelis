package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controlcommands "github.com/caelis-labs/caelis/ports/controlcommand"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/surfaces/transcript"
)

type agentRosterServices interface {
	controlagents.Connector
	controlagents.Disconnector
}

func dispatchSlashCommand(service ControlServices, sender *ProgramSender, text string) TaskResultMsg {
	return dispatchSlashCommandWithContext(context.Background(), service, sender, text, nil)
}

func dispatchSlashCommandWithContext(ctx context.Context, service ControlServices, sender *ProgramSender, text string, attachments []Attachment) TaskResultMsg {
	return dispatchSlashCommandWithContextResult(ctx, service, sender, text, attachments).completion
}

func dispatchSlashCommandWithContextResult(ctx context.Context, service ControlServices, sender *ProgramSender, text string, attachments []Attachment) executeLineResult {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	cmd, args, _, _ := controlprompt.ParseSlash(text)
	return dispatchTUIPrivateSlashCommandWithContext(ctx, service, sender, cmd, args)
}

func dispatchTUIPrivateSlashCommandWithContext(ctx context.Context, service ControlServices, sender *ProgramSender, cmd string, args string) executeLineResult {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()

	switch cmd {
	case "plugin":
		return executeLineResult{completion: slashPluginWithContext(ctx, service, send, args)}
	case "connect":
		return executeLineResult{completion: slashConnectWithContext(ctx, service, service, send, args)}
	case "subagent":
		return executeLineResult{completion: slashSubagentWithContext(ctx, service, send, args)}
	case "exit", "quit":
		return executeLineResult{completion: TaskResultMsg{ExitNow: true}}
	default:
		sendNotice(send, fmt.Sprintf("unknown TUI command: /%s", cmd))
		return executeLineResult{completion: TaskResultMsg{SuppressTurnDivider: true}}
	}
}

func executeTUIPrivateSlashCommandWithContext(ctx context.Context, service ControlServices, sender *ProgramSender, cmd string, args string) (executeLineResult, bool) {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if cmd == "" {
		return executeLineResult{}, false
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

func slashConnect(service control.Service, agents agentRosterServices, send func(tea.Msg), args string) TaskResultMsg {
	return slashConnectWithContext(context.Background(), service, agents, send, args)
}

func slashConnectWithContext(ctx context.Context, service control.Service, agents agentRosterServices, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	kind, payloadText, _ := controlprompt.ParseFirst(strings.TrimSpace(args))
	if strings.EqualFold(strings.TrimSpace(kind), "disconnect") {
		agentID, confirmation, _ := controlprompt.ParseFirst(payloadText)
		if strings.TrimSpace(agentID) == "" || !strings.EqualFold(strings.TrimSpace(confirmation), "confirmed") {
			sendNotice(send, "run /connect disconnect to choose and confirm a local ACP Agent")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		if agents == nil {
			return TaskResultMsg{Err: friendlyCommandError("disconnect ACP Agent", fmt.Errorf("ACP Agent roster service is unavailable"))}
		}
		result, err := agents.DisconnectACP(ctx, agentID)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("disconnect ACP Agent", err)}
		}
		message := "disconnected /" + strings.TrimSpace(result.Agent.ID)
		if result.ConnectionRemoved {
			message += "; Caelis connection settings were removed"
		} else {
			message += "; the shared ACP connection remains"
		}
		message += "; the installed adapter was kept"
		sendNotice(send, message)
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	if strings.EqualFold(strings.TrimSpace(kind), "acp") {
		payload, err := parseACPConnectWizardPayload(payloadText)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("connect ACP agent", err)}
		}
		if agents == nil {
			return TaskResultMsg{Err: friendlyCommandError("connect ACP agent", fmt.Errorf("ACP connection service is unavailable"))}
		}
		result, err := agents.ConnectACP(ctx, controlagents.ConnectRequest{
			AdapterID: payload.Agent, Launcher: payload.Launcher,
			CommandLine: payload.CommandLine, ModelID: payload.Model,
			ConfigValues: payload.ConfigValues,
		})
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("connect ACP agent", err)}
		}
		names := make([]string, 0, len(result.Agents))
		for _, agent := range result.Agents {
			name := "/" + strings.TrimSpace(agent.ID)
			if model := strings.TrimSpace(agent.Defaults.ModelID); model != "" {
				name += " (" + model + ")"
			}
			names = append(names, name)
		}
		sendNotice(send, "connected Agents: "+strings.Join(names, ", "))
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	}
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
	aliases := connectedModelAliases(cfg)
	connected := strings.Join(aliases, ", ")
	if connected == "" {
		connected = strings.TrimSpace(cfg.Model)
	}
	sendNotice(send, fmt.Sprintf(
		"connected: %s\nnext: /model use <model> [effort] switches the active model or reasoning effort",
		connected,
	))
	sendStatusUpdate(send, status)
	refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
	return TaskResultMsg{SuppressTurnDivider: true}
}

func connectedModelAliases(cfg control.ConnectConfig) []string {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if template, ok := modelconfig.LookupProvider(provider); ok {
		provider = template.Provider
	}
	models := strings.Split(cfg.Model, ",")
	aliases := make([]string, 0, len(models))
	seen := map[string]struct{}{}
	for _, name := range models {
		alias := modelconfig.BuildAlias(provider, name)
		if alias == "" {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	return aliases
}
