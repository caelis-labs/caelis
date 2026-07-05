package controlpromptrouter

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	controlcommands "github.com/caelis-labs/caelis/ports/controlcommand"
	prompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func (r Router) dispatchSlash(ctx context.Context, cmd string, args string, argsStart int, fullText string, attachments []control.Attachment) (prompt.Result, error) {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "help":
		names := r.helpCommandNames(ctx)
		help := controlcommands.HelpSnapshot(names)
		return r.slashResult(control.NewHelpSlashResult(help)), nil
	case "agent":
		return r.dispatchAgent(ctx, args)
	case "subagent":
		return r.dispatchSubagent(ctx, args)
	case "review":
		return r.dispatchReview(ctx, args, argsStart, fullText, attachments)
	case "new":
		return r.dispatchNew(ctx)
	case "resume":
		return r.dispatchResume(ctx, args)
	case "status":
		return r.dispatchStatus(ctx, args)
	case "doctor":
		return r.dispatchDoctor(ctx, args)
	case "model":
		return r.dispatchModel(ctx, args)
	case "compact":
		return r.dispatchCompact(ctx, args)
	}
	return r.dispatchDynamicAgent(ctx, cmd, args, prompt.AttachmentsForPromptRange(attachments, argsStart, len([]rune(strings.TrimSpace(fullText)))))
}

func (r Router) dispatchAgent(ctx context.Context, args string) (prompt.Result, error) {
	sub, rest, _ := prompt.ParseFirst(strings.TrimSpace(args))
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "", "help":
		return r.noticeResult(controlcommands.AgentHelpText()), nil
	case "list":
		agents, err := r.service.ListAgents(ctx, 20)
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("agent list", err)
		}
		status, _ := r.service.AgentStatus(ctx)
		return r.noticeResult(controlcommands.FormatAgentList(agents, status)), nil
	case "status":
		return r.noticeResult("usage: /agent list | add <builtin> | use <agent|local> | remove <agent>"), nil
	case "add":
		addArgs, ok := controlcommands.ParseAgentAddArgs(rest)
		if !ok || addArgs.Target == "" {
			return r.noticeResult("usage: /agent add <name> | /agent add custom <name> -- <command> [args...]"), nil
		}
		if addArgs.Install {
			return r.noticeResult("usage: /agent add <name> | /agent add custom <name> -- <command> [args...]"), nil
		}
		_, err := r.service.AddAgentWithOptions(ctx, addArgs.Target, control.AgentAddOptions{
			Custom: addArgs.Custom,
		})
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("agent add", err)
		}
		result := r.noticeResult(controlcommands.FormatAgentReadyNotice(addArgs.Target))
		result.RefreshCommands = true
		return result, nil
	case "remove":
		target := strings.TrimSpace(rest)
		if target == "" {
			return r.noticeResult("usage: /agent remove <agent>\nrun /agent list to inspect registered agents"), nil
		}
		_, err := r.service.RemoveAgent(ctx, target)
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("agent remove", err)
		}
		result := r.noticeResult(controlcommands.FormatAgentRemovedNotice(target))
		result.RefreshCommands = true
		return result, nil
	case "use":
		target := strings.TrimSpace(rest)
		if target == "" {
			return r.noticeResult("usage: /agent use <agent|local>\nrun /agent list for registered agents"), nil
		}
		status, err := r.service.HandoffAgent(ctx, target)
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("agent use", err)
		}
		result := r.noticeResult(controlcommands.FormatAgentUseNotice(target, status))
		if current, err := r.service.Status(ctx); err == nil {
			result.StatusUpdate = &current
		}
		result.RefreshCommands = true
		return result, nil
	default:
		return r.noticeResult("usage: /agent list | add <builtin> | use <agent|local> | remove <agent>"), nil
	}
}

func (r Router) dispatchSubagent(ctx context.Context, args string) (prompt.Result, error) {
	sub, rest, _ := prompt.ParseFirst(strings.TrimSpace(args))
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "", "help":
		return r.noticeResult(subagentUsageText()), nil
	case "list":
		status, err := r.service.AgentProfileStatus(ctx)
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("subagent list", err)
		}
		return r.slashResult(control.NewSubagentProfilesSlashResult(status)), nil
	case "run":
		return r.noticeResult(strings.Join([]string{
			"/subagent run has been removed.",
			"Use /review [instructions] for code review.",
			"Use /<agent> <prompt> for a registered ACP side agent.",
		}, "\n")), nil
	case "bind":
		cfg, ok := parseSubagentBindArgs(rest)
		if !ok {
			return r.noticeResult(subagentBindUsageText()), nil
		}
		status, err := r.service.BindAgentProfile(ctx, cfg)
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("subagent bind", err)
		}
		result := r.noticeResult(control.FormatSubagentBindNotice(cfg.ProfileID, status))
		result.RefreshCommands = true
		return result, nil
	default:
		return r.noticeResult(subagentUsageText()), nil
	}
}

func (r Router) dispatchReview(ctx context.Context, args string, argsStart int, fullText string, attachments []control.Attachment) (prompt.Result, error) {
	promptAttachments := prompt.AttachmentsForPromptRange(attachments, argsStart, len([]rune(strings.TrimSpace(fullText))))
	turn, err := r.service.StartReviewSubagent(ctx, strings.TrimSpace(args), promptAttachments)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("review", err)
	}
	return prompt.Result{Handled: true, Turn: turn}, nil
}

func (r Router) dispatchNew(ctx context.Context) (prompt.Result, error) {
	session, err := r.service.NewSession(ctx)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("new session", err)
	}
	result := r.noticeResult(fmt.Sprintf("new session: %s", session.SessionID))
	result.ClearHistory = true
	if status, err := r.service.Status(ctx); err == nil {
		result.StatusUpdate = &status
	}
	return result, nil
}

func (r Router) dispatchResume(ctx context.Context, args string) (prompt.Result, error) {
	sessionID := strings.TrimSpace(args)
	if sessionID == "" {
		candidates, err := r.service.ListSessions(ctx, 10)
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("list sessions", err)
		}
		if len(candidates) == 0 {
			return r.noticeResult("no sessions available to resume"), nil
		}
		lines := []string{"available sessions:"}
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
		return r.noticeResult(strings.Join(lines, "\n")), nil
	}
	if _, err := r.service.ResumeSession(ctx, sessionID); err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("resume session", err)
	}
	result := prompt.Result{Handled: true, ClearHistory: true, SuppressTurnDivider: true}
	if events, err := r.service.ReplayEvents(ctx); err == nil {
		result.ReplayEvents = events
	} else {
		result.Events = append(result.Events, notice(fmt.Sprintf("warning: replay failed: %v", err)))
	}
	if status, err := r.service.Status(ctx); err == nil {
		result.StatusUpdate = &status
	}
	return result, nil
}

func (r Router) dispatchStatus(ctx context.Context, args string) (prompt.Result, error) {
	if strings.TrimSpace(args) != "" {
		return r.noticeResult("usage: /status"), nil
	}
	status, err := r.service.Status(ctx)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("status", err)
	}
	return r.slashResult(control.NewStatusSlashResult(status)), nil
}

func (r Router) dispatchDoctor(ctx context.Context, args string) (prompt.Result, error) {
	if strings.TrimSpace(args) != "" {
		return r.noticeResult("usage: /doctor"), nil
	}
	status, err := r.service.Status(ctx)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("doctor", err)
	}
	result := prompt.Result{Handled: true, SuppressTurnDivider: true}
	setup := control.SandboxSetupViewFromStatus(status)
	if setup.RepairRequired {
		result.Events = append(result.Events, notice("Windows sandbox repair started. Approve the UAC prompt if shown."))
		repairedStatus, err := r.service.RepairSandbox(ctx)
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("doctor", err)
		}
		status = repairedStatus
		setup = control.SandboxSetupViewFromStatus(status)
		if setup.AnyRequired {
			result.Events = append(result.Events, notice("Windows sandbox repair still needs attention. Run /doctor for details."))
		} else {
			result.Events = append(result.Events, notice("Windows sandbox repair complete."))
		}
	}
	result.Events = append(result.Events, notice(control.FormatDoctorSnapshot(status)))
	return result, nil
}

func (r Router) dispatchModel(ctx context.Context, args string) (prompt.Result, error) {
	sub, rest, _ := prompt.ParseFirst(strings.TrimSpace(args))
	_, activeACP := control.ActiveACPStatus(ctx, r.service)
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "use":
		alias, reasoning := parseModelUseArgs(rest)
		if alias == "" {
			if activeACP {
				return r.noticeResult("usage: /model use <model> [effort]"), nil
			}
			return r.noticeResult("usage: /model use <alias>"), nil
		}
		status, err := r.service.UseModel(ctx, alias, reasoning)
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("model use", err)
		}
		text := fmt.Sprintf("model switched to: %s", status.ModelStatus.Display)
		if strings.TrimSpace(reasoning) != "" {
			text = fmt.Sprintf("model switched to: %s (reasoning: %s)", status.ModelStatus.Display, reasoning)
		}
		result := r.noticeResult(text)
		result.StatusUpdate = &status
		return result, nil
	case "del", "delete", "rm":
		if activeACP {
			return r.noticeResult("usage: /model use <model> [effort]"), nil
		}
		alias := strings.TrimSpace(rest)
		if alias == "" {
			return r.noticeResult("usage: /model del <alias>"), nil
		}
		if err := r.service.DeleteModel(ctx, alias); err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("model delete", err)
		}
		result := r.noticeResult(fmt.Sprintf("model deleted: %s", alias))
		if status, err := r.service.Status(ctx); err == nil {
			result.StatusUpdate = &status
		}
		return result, nil
	default:
		if activeACP {
			return r.noticeResult("usage: /model use <model> [effort]"), nil
		}
		return r.noticeResult("usage: /model use|del <alias>"), nil
	}
}

func (r Router) dispatchCompact(ctx context.Context, args string) (prompt.Result, error) {
	if strings.TrimSpace(args) != "" {
		return r.noticeResult("usage: /compact"), nil
	}
	if err := r.service.Compact(ctx); err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("compact", err)
	}
	return r.noticeResult(compact.CompactNoticeLabel), nil
}

func (r Router) dispatchDynamicAgent(ctx context.Context, agent string, promptText string, attachments []control.Attachment) (prompt.Result, error) {
	agent = strings.ToLower(strings.TrimSpace(agent))
	promptText = strings.TrimSpace(promptText)
	if promptText == "" && len(attachments) == 0 {
		if r.isRegisteredAgent(ctx, agent) {
			return r.noticeResult(fmt.Sprintf("usage: /%s <prompt>", agent)), nil
		}
		return prompt.Result{}, nil
	}
	turn, err := r.service.StartAgentSubagent(ctx, agent, promptText, attachments)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("/"+agent, err)
	}
	return prompt.Result{Handled: true, Turn: turn}, nil
}

func (r Router) dispatchMention(ctx context.Context, text string, attachments []control.Attachment) (prompt.Result, error) {
	handle, promptText, promptStart := prompt.ParseFirst(strings.TrimSpace(text))
	handle = strings.TrimPrefix(strings.TrimSpace(handle), "@")
	attachments = prompt.AttachmentsForPromptRange(attachments, promptStart, len([]rune(strings.TrimSpace(text))))
	if handle == "" || (strings.TrimSpace(promptText) == "" && len(attachments) == 0) {
		return r.noticeResult("usage: @handle <prompt>"), nil
	}
	turn, err := r.service.ContinueSubagent(ctx, handle, promptText, attachments)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("@"+handle, err)
	}
	return prompt.Result{Handled: true, Turn: turn}, nil
}

func parseModelUseArgs(args string) (alias string, reasoning string) {
	alias, rest, _ := prompt.ParseFirst(strings.TrimSpace(args))
	return strings.TrimSpace(alias), strings.TrimSpace(rest)
}
