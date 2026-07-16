package controlpromptrouter

import (
	"context"
	"fmt"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	controlcommands "github.com/caelis-labs/caelis/ports/controlcommand"
	prompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// Router dispatches surface-neutral prompt input through control.Service.
type Router struct {
	service               control.Service
	commandNames          func(context.Context, control.Service) []string
	coreCommandAllowed    func(context.Context, string) bool
	dynamicCommandAllowed func(context.Context, string) bool
	privateSlashHandler   prompt.PrivateSlashHandler
}

func New(cfg prompt.RouterConfig) prompt.Router {
	return Router{
		service:               cfg.Service,
		commandNames:          cfg.CommandNames,
		coreCommandAllowed:    cfg.CoreCommandAllowed,
		dynamicCommandAllowed: cfg.DynamicCommandAllowed,
		privateSlashHandler:   cfg.PrivateSlashHandler,
	}
}

func (r Router) Route(ctx context.Context, req prompt.Request) (prompt.Result, error) {
	ctx = contextOrBackground(ctx)
	if r.service == nil {
		return prompt.Result{}, fmt.Errorf("control prompt: service is required")
	}
	text := strings.TrimSpace(req.Submission.Text)
	if cmd, args, argsStart, ok := prompt.ParseSlash(text); ok {
		if result, handled, err := r.dispatchPrivateSlash(ctx, prompt.PrivateSlashRequest{
			Command:     cmd,
			Args:        args,
			ArgsStart:   argsStart,
			FullText:    text,
			Attachments: req.Submission.Attachments,
		}); handled || err != nil {
			return result, err
		}
		if !r.shouldDispatchSlash(ctx, cmd) {
			if r.isRemoteControllerCommand(ctx, cmd) {
				turn, err := r.service.Submit(ctx, req.Submission)
				if err != nil {
					return prompt.Result{}, controlcommands.FriendlyCommandError("submit", err)
				}
				if turn == nil {
					return prompt.Result{Handled: true, ContinueRunning: true, SuppressTurnDivider: true}, nil
				}
				return prompt.Result{Handled: true, Turn: turn}, nil
			}
			return r.noticeResult(fmt.Sprintf("unknown command: /%s\nrun /help to list available commands", cmd)), nil
		}
		return r.dispatchSlash(ctx, cmd, args, argsStart, text, req.Submission.Attachments)
	}
	turn, err := r.service.Submit(ctx, req.Submission)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("submit", err)
	}
	if turn == nil {
		return prompt.Result{Handled: true, ContinueRunning: true, SuppressTurnDivider: true}, nil
	}
	return prompt.Result{Handled: true, Turn: turn}, nil
}

func (r Router) shouldDispatchSlash(ctx context.Context, cmd string) bool {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if cmd == "" {
		return false
	}
	if controlcommands.IsSharedKnown(cmd) {
		if !r.coreSlashAllowed(ctx, cmd) {
			return false
		}
		if r.coreCommandAllowed != nil {
			// ACP routers are already narrowed to their exposed core command set,
			// so they intentionally bypass the TUI active-controller gate.
			return true
		}
		if control.ACPControllerActive(ctx, r.service) {
			return controlcommands.IsLocalDuringACP(cmd)
		}
		return true
	}
	if r.dynamicCommandAllowed != nil {
		if r.isDirectAgentRun(ctx, cmd) {
			if agent, _, ok := controlagents.ParseRunName(cmd); ok {
				return r.dynamicSlashAllowed(ctx, agent)
			}
		}
		return r.dynamicSlashAllowed(ctx, cmd)
	}
	return controldelegation.IsDirectRunProfile(cmd) || r.isDirectAgentRun(ctx, cmd)
}

func (r Router) dispatchPrivateSlash(ctx context.Context, req prompt.PrivateSlashRequest) (prompt.Result, bool, error) {
	if r.privateSlashHandler == nil {
		return prompt.Result{}, false, nil
	}
	result, handled, err := r.privateSlashHandler(contextOrBackground(ctx), req)
	if !handled || err != nil {
		return result, handled, err
	}
	if !result.Handled {
		result.Handled = true
	}
	return result, true, nil
}

func (r Router) coreSlashAllowed(ctx context.Context, cmd string) bool {
	if r.coreCommandAllowed == nil {
		return true
	}
	return r.coreCommandAllowed(contextOrBackground(ctx), strings.ToLower(strings.TrimSpace(cmd)))
}

func (r Router) dynamicSlashAllowed(ctx context.Context, cmd string) bool {
	if r.dynamicCommandAllowed == nil {
		return true
	}
	return r.dynamicCommandAllowed(contextOrBackground(ctx), strings.ToLower(strings.TrimSpace(cmd)))
}

func (r Router) helpCommandNames(ctx context.Context) []string {
	if r.commandNames != nil {
		return r.commandNames(ctx, r.service)
	}
	return controlcommands.DefaultSharedNames()
}

func (r Router) noticeResult(text string) prompt.Result {
	return prompt.Result{
		Handled:             true,
		Events:              []eventstream.Envelope{notice(text)},
		SuppressTurnDivider: true,
	}
}

func (r Router) slashResult(result control.SlashCommandResult) prompt.Result {
	return prompt.Result{
		Handled:             true,
		SlashResult:         &result,
		SuppressTurnDivider: true,
	}
}

func (r Router) isDirectAgentRun(ctx context.Context, name string) bool {
	status, err := r.service.AgentStatus(contextOrBackground(ctx))
	if err != nil {
		return false
	}
	return controlagents.RunNameAllowed(directAgentRuns(status), name, availableAgentCommandName)
}

func (r Router) isRemoteControllerCommand(ctx context.Context, name string) bool {
	name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/")))
	if name == "" || controlcommands.IsKnown(name) || strings.EqualFold(name, "lead") {
		return false
	}
	status, err := r.service.AgentStatus(contextOrBackground(ctx))
	if err != nil || !strings.EqualFold(strings.TrimSpace(status.ControllerKind), "acp") {
		return false
	}
	baseName := name
	if agent, _, ok := controlagents.ParseRunName(name); ok {
		baseName = agent
	}
	for _, agent := range status.AvailableAgents {
		if controlagents.NormalizeName(agent.Name) == baseName {
			return false
		}
	}
	for _, advertised := range status.ControllerCommands {
		advertised = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(advertised, "/")))
		if fields := strings.Fields(advertised); len(fields) > 0 && fields[0] == name {
			return true
		}
	}
	return false
}

func directAgentRuns(status control.AgentStatusSnapshot) []controlagents.Run {
	runs := make([]controlagents.Run, 0, len(status.Participants))
	for _, participant := range status.Participants {
		runs = append(runs, controldelegation.DirectRunFromParticipant(participant.Label, participant.Kind, participant.Role, participant.Source))
	}
	return runs
}

func availableAgentCommandName(name string) bool {
	return controldelegation.IsDirectRunProfile(name)
}

func notice(text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:   eventstream.KindNotice,
		Notice: strings.TrimSpace(text),
	}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
