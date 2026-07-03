package tuiapp

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	controlcommands "github.com/caelis-labs/caelis/ports/controlcommand"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/transcript"
)

func isTUIPrivateAgentSlash(args string) bool {
	sub, rest, _ := controlprompt.ParseFirst(strings.TrimSpace(args))
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "install":
		return true
	case "add":
		addArgs, ok := controlcommands.ParseAgentAddArgs(rest)
		return ok && addArgs.Install && addArgs.Target != ""
	default:
		return false
	}
}

func slashAgentPrivateWithContext(ctx context.Context, service control.Service, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	sub, rest, _ := controlprompt.ParseFirst(strings.TrimSpace(args))
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "install":
		return slashAgentInstallWithContext(ctx, service, send, strings.TrimSpace(rest))
	case "add":
		addArgs, ok := controlcommands.ParseAgentAddArgs(rest)
		if ok && addArgs.Install && addArgs.Target != "" {
			return slashAgentInstallWithContext(ctx, service, send, addArgs.Target)
		}
	}
	sendNotice(send, "usage: /agent install <adapter>")
	return TaskResultMsg{SuppressTurnDivider: true}
}

func slashAgentInstallWithContext(ctx context.Context, service control.Service, send func(tea.Msg), target string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	target = strings.TrimSpace(target)
	if target == "" {
		sendNotice(send, "usage: /agent install <adapter>")
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	command := agentInstallCommandForDisplay(ctx, service, target)
	callID := sendAgentInstallToolCall(send, target, command)
	_, err := service.AddAgentWithOptions(ctx, target, control.AgentAddOptions{Install: true})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			sendAgentInstallToolResult(send, callID, command, transcript.ToolStatusInterrupted, false, agentInstallErrorOutput(err))
			return TaskResultMsg{Interrupted: true, SuppressTurnDivider: true}
		}
		sendAgentInstallToolResult(send, callID, command, schema.ToolStatusFailed, true, agentInstallErrorOutput(err))
		return TaskResultMsg{Err: friendlyCommandError("agent install", err)}
	}
	sendAgentInstallToolResult(send, callID, command, schema.ToolStatusCompleted, false, "")
	sendNotice(send, controlcommands.FormatAgentReadyNotice(target))
	refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
	return TaskResultMsg{SuppressTurnDivider: true}
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
	meta := agentInstallToolMeta()
	if renderableTextHasContent(output) {
		meta = metautil.WithTerminalOutput(meta, callID, output)
	} else {
		meta = metautil.WithTerminalInfo(meta, callID)
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
			Meta:          meta,
		},
	})
}

func agentInstallACPStatus(status string) string {
	switch status {
	case transcript.ToolStatusStarted, transcript.ToolStatusRunning:
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
