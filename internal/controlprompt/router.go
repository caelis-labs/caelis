package controlprompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// router dispatches surface-neutral prompt input through RouterService.
type router struct {
	service               RouterService
	skillResolver         SkillResolver
	commandNames          func(context.Context, RouterService) []string
	coreCommandAllowed    func(context.Context, string) bool
	dynamicCommandAllowed func(context.Context, string) bool
	privateSlashHandler   PrivateSlashHandler
}

// New builds the shared surface-neutral prompt router.
func New(cfg RouterConfig) Router {
	resolver, _ := cfg.Service.(SkillResolver)
	return router{
		service:               cfg.Service,
		skillResolver:         resolver,
		commandNames:          cfg.CommandNames,
		coreCommandAllowed:    cfg.CoreCommandAllowed,
		dynamicCommandAllowed: cfg.DynamicCommandAllowed,
		privateSlashHandler:   cfg.PrivateSlashHandler,
	}
}

func (r router) Route(ctx context.Context, req Request) (Result, error) {
	ctx = contextOrBackground(ctx)
	if r.service == nil {
		return Result{}, fmt.Errorf("control prompt: service is required")
	}
	text := strings.TrimSpace(req.Submission.Text)
	if cmd, args, argsStart, ok := ParseSlash(text); ok {
		if result, handled, err := r.dispatchPrivateSlash(ctx, PrivateSlashRequest{
			Command:     cmd,
			Args:        args,
			ArgsStart:   argsStart,
			FullText:    text,
			Attachments: req.Submission.Attachments,
		}); handled || err != nil {
			return result, err
		}
		if r.shouldDispatchSlash(ctx, cmd) {
			return r.dispatchSlash(ctx, cmd, args, argsStart, text, req.Submission.Attachments)
		}
		if result, handled, err := r.dispatchDirectSkill(ctx, cmd, args, argsStart, req.Submission); handled || err != nil {
			return result, err
		}
		// Best-effort: slash-like prose that does not exactly resolve a
		// configured command or skill is an ordinary prompt.
	}
	turn, err := r.service.Submit(ctx, req.Submission)
	if err != nil {
		return Result{}, FriendlyCommandError("submit", err)
	}
	if turn == nil {
		return Result{Handled: true, ContinueRunning: true, SuppressTurnDivider: true}, nil
	}
	return Result{Handled: true, Turn: turn}, nil
}

func (r router) shouldDispatchSlash(ctx context.Context, cmd string) bool {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if cmd == "" {
		return false
	}
	if IsSharedKnown(cmd) {
		if !r.coreSlashAllowed(ctx, cmd) {
			return false
		}
		if r.coreCommandAllowed != nil {
			return true
		}
		return true
	}
	if r.dynamicCommandAllowed != nil {
		if r.isDirectAgentRun(ctx, cmd) {
			if agent, _, ok := controlagents.ParseRunName(cmd); ok {
				return r.dynamicSlashAllowed(ctx, agent)
			}
		}
		return r.isConfiguredDirectHandle(ctx, cmd) && r.dynamicSlashAllowed(ctx, cmd)
	}
	return r.isConfiguredDirectHandle(ctx, cmd) || r.isDirectAgentRun(ctx, cmd)
}

func (r router) dispatchPrivateSlash(ctx context.Context, req PrivateSlashRequest) (Result, bool, error) {
	if r.privateSlashHandler == nil {
		return Result{}, false, nil
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

func (r router) coreSlashAllowed(ctx context.Context, cmd string) bool {
	if r.coreCommandAllowed == nil {
		return true
	}
	return r.coreCommandAllowed(contextOrBackground(ctx), strings.ToLower(strings.TrimSpace(cmd)))
}

func (r router) dynamicSlashAllowed(ctx context.Context, cmd string) bool {
	if r.dynamicCommandAllowed == nil {
		return true
	}
	return r.dynamicCommandAllowed(contextOrBackground(ctx), strings.ToLower(strings.TrimSpace(cmd)))
}

func (r router) helpCommandNames(ctx context.Context) []string {
	if r.commandNames != nil {
		return r.commandNames(ctx, r.service)
	}
	return DefaultSharedNames()
}

func (r router) noticeResult(text string) Result {
	return Result{
		Handled:             true,
		Events:              []eventstream.Envelope{notice(text)},
		SuppressTurnDivider: true,
	}
}

func (r router) slashResult(result SlashCommandResult) Result {
	return Result{
		Handled:             true,
		SlashResult:         &result,
		SuppressTurnDivider: true,
	}
}

func (r router) isDirectAgentRun(ctx context.Context, name string) bool {
	status, err := r.service.AgentStatus(contextOrBackground(ctx))
	if err != nil {
		return false
	}
	return controlagents.RunNameAllowed(directAgentRuns(status), name, nil)
}

func (r router) isConfiguredDirectHandle(ctx context.Context, name string) bool {
	handle := agentbinding.NormalizeHandle(agentbinding.Handle(name))
	bindings, ok := r.service.(agentbinding.Service)
	if !ok {
		return agentbinding.IsDirectRun(handle)
	}
	status, err := bindings.AgentBindingStatus(contextOrBackground(ctx))
	if err != nil {
		return false
	}
	return agentbinding.IsBoundDirectHandle(status, handle)
}

func directAgentRuns(status AgentStatusSnapshot) []controlagents.Run {
	runs := make([]controlagents.Run, 0, len(status.Participants))
	for _, participant := range status.Participants {
		runs = append(runs, controlagents.DirectRunFromParticipant(participant.Label, participant.Kind, participant.Role, participant.Source))
	}
	return runs
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
