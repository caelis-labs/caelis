package controlpromptrouter

import (
	"context"
	"errors"
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
	case "review":
		return r.dispatchReview(ctx, args, argsStart, fullText, attachments)
	case "lead":
		return r.dispatchLead(ctx, args)
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
	return r.dispatchAgentRun(ctx, cmd, args, prompt.AttachmentsForPromptRange(attachments, argsStart, len([]rune(strings.TrimSpace(fullText)))))
}

func (r Router) dispatchLead(ctx context.Context, args string) (prompt.Result, error) {
	target := strings.TrimSpace(args)
	if target == "" {
		return r.noticeResult("usage: /lead <agent|local>"), nil
	}
	status, err := r.service.HandoffAgent(ctx, target)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("lead", err)
	}
	label := strings.TrimSpace(status.ControllerLabel)
	if label == "" {
		label = target
	}
	text := label + " is now leading this task"
	if controlcommands.IsLocalAgentTarget(target) {
		text = "local Agent is now leading this task"
	}
	result := r.noticeResult(text)
	if current, statusErr := r.service.Status(ctx); statusErr == nil {
		result.StatusUpdate = &current
	}
	result.RefreshCommands = true
	return result, nil
}

func (r Router) dispatchReview(ctx context.Context, args string, argsStart int, fullText string, attachments []control.Attachment) (prompt.Result, error) {
	promptAttachments := prompt.AttachmentsForPromptRange(attachments, argsStart, len([]rune(strings.TrimSpace(fullText))))
	turn, err := r.service.StartReview(ctx, strings.TrimSpace(args), promptAttachments)
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
	result.ActiveSessionID = strings.TrimSpace(session.SessionID)
	result.RefreshStatus = true
	result.RefreshCommands = true
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
	resumed, err := r.service.ResumeSession(ctx, sessionID)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("resume session", err)
	}
	result := prompt.Result{
		Handled:             true,
		ClearHistory:        true,
		SuppressTurnDivider: true,
		ActiveSessionID:     strings.TrimSpace(resumed.SessionID),
		RefreshStatus:       true,
		RefreshCommands:     true,
		Reconnect:           resumed.Reconnect,
	}
	if resumed.Reconnect == nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError(
			"resume session", errors.New("control reconnect continuation is unavailable"),
		)
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
	result := r.slashResult(control.NewStatusSlashResult(status))
	result.StatusUpdate = &status
	return result, nil
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
		result.RefreshCommands = true
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
	result := r.noticeResult(compact.CompactNoticeLabel)
	if status, err := r.service.Status(ctx); err == nil {
		result.StatusUpdate = &status
	}
	return result, nil
}

func (r Router) dispatchAgentRun(ctx context.Context, command string, promptText string, attachments []control.Attachment) (prompt.Result, error) {
	command = strings.ToLower(strings.TrimSpace(command))
	promptText = strings.TrimSpace(promptText)
	if promptText == "" && len(attachments) == 0 {
		if r.isRegisteredAgent(ctx, command) || r.isDirectAgentRun(ctx, command) {
			return r.noticeResult(fmt.Sprintf("usage: /%s <prompt>", command)), nil
		}
		return r.noticeResult(fmt.Sprintf("unknown command: /%s\nrun /help to list available commands", command)), nil
	}
	if r.isDirectAgentRun(ctx, command) {
		turn, err := r.service.ContinueAgentRun(ctx, command, promptText, attachments)
		if err != nil {
			return prompt.Result{}, controlcommands.FriendlyCommandError("/"+command, err)
		}
		return prompt.Result{Handled: true, Turn: turn}, nil
	}
	turn, err := r.service.StartAgentRun(ctx, command, promptText, attachments)
	if err != nil {
		return prompt.Result{}, controlcommands.FriendlyCommandError("/"+command, err)
	}
	return prompt.Result{Handled: true, Turn: turn, RefreshCommands: true}, nil
}

func parseModelUseArgs(args string) (alias string, reasoning string) {
	alias, rest, _ := prompt.ParseFirst(strings.TrimSpace(args))
	return strings.TrimSpace(alias), strings.TrimSpace(rest)
}
