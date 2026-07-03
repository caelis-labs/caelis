package controlpromptrouter

import (
	"context"
	"fmt"
	"strings"

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

func (r Router) StreamSubscriber() (control.StreamSubscriber, bool) {
	streamer, ok := r.service.(control.StreamSubscriber)
	return streamer, ok
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
			return prompt.Result{}, nil
		}
		return r.dispatchSlash(ctx, cmd, args, argsStart, text, req.Submission.Attachments)
	}
	if strings.HasPrefix(text, "@") {
		return r.dispatchMention(ctx, text, req.Submission.Attachments)
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
		return r.dynamicSlashAllowed(ctx, cmd)
	}
	return r.isRegisteredAgent(ctx, cmd)
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
	return controlcommands.AppendRegisteredAgentNames(ctx, r.service, controlcommands.DefaultSharedNames())
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

func (r Router) isRegisteredAgent(ctx context.Context, agent string) bool {
	return controlcommands.RegisteredAgentNameAllowed(ctx, r.service, agent)
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

func parseSubagentBindArgs(args string) (control.AgentProfileBindingConfig, bool) {
	fields := strings.Fields(strings.TrimSpace(args))
	if len(fields) < 2 {
		return control.AgentProfileBindingConfig{}, false
	}
	cfg := control.AgentProfileBindingConfig{ProfileID: fields[0]}
	switch strings.ToLower(strings.TrimSpace(fields[1])) {
	case "default", "self", "builtin", "built-in":
		if len(fields) != 2 {
			return control.AgentProfileBindingConfig{}, false
		}
		cfg.Target = "built_in"
		return cfg, true
	case "model":
		if len(fields) < 3 || len(fields) > 4 {
			return control.AgentProfileBindingConfig{}, false
		}
		cfg.Target = "built_in"
		cfg.Model = fields[2]
		if len(fields) == 4 {
			cfg.ReasoningEffort = fields[3]
		}
		return cfg, true
	case "acp":
		if len(fields) != 3 {
			return control.AgentProfileBindingConfig{}, false
		}
		cfg.Target = "acp"
		cfg.ACPAgent = fields[2]
		return cfg, true
	default:
		return control.AgentProfileBindingConfig{}, false
	}
}

func subagentUsageText() string {
	return strings.Join([]string{
		"usage:",
		"  /subagent list",
		"  /subagent bind <id> default",
		"  /subagent bind <id> model <alias> [reasoning]",
		"  /subagent bind <id> acp <agent>",
	}, "\n")
}

func subagentBindUsageText() string {
	return strings.Join([]string{
		"usage:",
		"  /subagent bind <id> default",
		"  /subagent bind <id> model <alias> [reasoning]",
		"  /subagent bind <id> acp <agent>",
	}, "\n")
}
