package prompt

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	controlcommands "github.com/OnslaughtSnail/caelis/protocol/acp/control/commands"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
)

// Router dispatches surface-neutral prompt input through control.Service.
type Router struct {
	service               control.Service
	commandNames          func(context.Context, control.Service) []string
	coreCommandAllowed    func(context.Context, string) bool
	dynamicCommandAllowed func(context.Context, string) bool
	privateSlashHandler   PrivateSlashHandler
}

type Config struct {
	Service control.Service
	// CommandNames controls /help rendering. When nil, shared command names and
	// registered ACP agent commands are used.
	CommandNames func(context.Context, control.Service) []string
	// CoreCommandAllowed optionally narrows which shared core slash commands this
	// router may execute. Dynamic ACP agent slashes are checked separately.
	CoreCommandAllowed func(context.Context, string) bool
	// DynamicCommandAllowed optionally narrows which registered agent slash
	// commands this router may execute. When nil, all registered agents from the
	// control service are accepted.
	DynamicCommandAllowed func(context.Context, string) bool
	// PrivateSlashHandler handles surface-owned slash commands after ParseSlash
	// and before shared/dynamic routing. It keeps private commands on the same
	// parse path without making protocol/control depend on a surface type.
	PrivateSlashHandler PrivateSlashHandler
}

func NewRouter(cfg Config) Router {
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

type Request struct {
	Submission control.Submission
}

type PrivateSlashRequest struct {
	Command     string
	Args        string
	ArgsStart   int
	FullText    string
	Attachments []control.Attachment
}

type PrivateSlashHandler func(context.Context, PrivateSlashRequest) (Result, bool, error)

// Result is the surface-neutral outcome of routing one prompt submission.
//
// Events and ReplayEvents are already ACP/eventstream-shaped and should be
// forwarded by every surface that can display them. Turn carries live streaming
// work and remains owned by the caller until it is closed.
//
// The boolean fields are semantic side effects, not UI instructions. TUI maps
// ClearHistory to transcript clearing and StatusUpdate to its status bar; ACP
// maps those same intents to standard session/update state refreshes. Do not
// add wizard/modal rendering state here; interactive workflows stay owned by
// their surface. PrivateResult is only populated by a PrivateSlashHandler and
// must be interpreted by the surface that installed that handler.
type Result struct {
	Handled             bool
	Turn                control.Turn
	Events              []eventstream.Envelope
	ClearHistory        bool
	ReplayEvents        []eventstream.Envelope
	RefreshCommands     bool
	StatusUpdate        *control.StatusSnapshot
	SuppressTurnDivider bool
	ContinueRunning     bool
	PrivateResult       any
}

func (r Router) Route(ctx context.Context, req Request) (Result, error) {
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
		if !r.shouldDispatchSlash(ctx, cmd) {
			return Result{}, nil
		}
		return r.dispatchSlash(ctx, cmd, args, argsStart, text, req.Submission.Attachments)
	}
	if strings.HasPrefix(text, "@") {
		return r.dispatchMention(ctx, text, req.Submission.Attachments)
	}
	turn, err := r.service.Submit(ctx, req.Submission)
	if err != nil {
		return Result{}, controlcommands.FriendlyCommandError("submit", err)
	}
	if turn == nil {
		return Result{Handled: true, ContinueRunning: true, SuppressTurnDivider: true}, nil
	}
	return Result{Handled: true, Turn: turn}, nil
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

func (r Router) dispatchPrivateSlash(ctx context.Context, req PrivateSlashRequest) (Result, bool, error) {
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

func (r Router) noticeResult(text string) Result {
	return Result{
		Handled:             true,
		Events:              []eventstream.Envelope{notice(text)},
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

func ParseSlash(text string) (cmd, args string, argsStart int, ok bool) {
	textRunes := []rune(strings.TrimSpace(text))
	idx := 0
	if idx >= len(textRunes) || textRunes[idx] != '/' {
		return "", "", 0, false
	}
	idx++
	cmdStart := idx
	for idx < len(textRunes) && !unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	cmd = strings.TrimSpace(strings.ToLower(string(textRunes[cmdStart:idx])))
	for idx < len(textRunes) && unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	argsStart = idx
	args = strings.TrimSpace(string(textRunes[idx:]))
	return cmd, args, argsStart, cmd != ""
}

func ParseFirst(text string) (first, rest string, restStart int) {
	textRunes := []rune(strings.TrimSpace(text))
	idx := 0
	for idx < len(textRunes) && !unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	first = strings.TrimSpace(string(textRunes[:idx]))
	for idx < len(textRunes) && unicode.IsSpace(textRunes[idx]) {
		idx++
	}
	restStart = idx
	rest = strings.TrimSpace(string(textRunes[idx:]))
	return
}

func AttachmentsForPromptRange(items []control.Attachment, start int, end int) []control.Attachment {
	if len(items) == 0 {
		return nil
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	out := make([]control.Attachment, 0, len(items))
	for _, item := range items {
		if item.Offset < start || item.Offset > end {
			continue
		}
		out = append(out, control.Attachment{
			Name:     item.Name,
			Offset:   item.Offset - start,
			MimeType: item.MimeType,
			Data:     item.Data,
		})
	}
	return out
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
