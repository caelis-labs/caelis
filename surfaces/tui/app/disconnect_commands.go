package tuiapp

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func slashDisconnectWithContext(ctx context.Context, service ControlServices, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	kind, selection, _ := controlprompt.ParseFirst(strings.TrimSpace(args))
	targets := modelconfig.DedupeNonEmptyStrings(strings.Split(selection, ","))
	var completed []string
	var err error
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "provider":
		if len(targets) == 0 {
			sendNotice(send, "Choose provider models in /disconnect", SlashNoticeHint)
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		completed, err = service.DeleteModels(ctx, targets)
	case "acp":
		if len(targets) == 0 {
			sendNotice(send, "Choose local ACP Agents in /disconnect", SlashNoticeHint)
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		completed, err = service.DisconnectACPAgents(ctx, targets)
		for i := range completed {
			completed[i] = "/" + completed[i]
		}
	default:
		sendNotice(send, "usage: /disconnect", SlashNoticeHint)
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	if len(completed) > 0 {
		sendNotice(send, "Disconnected "+strings.Join(completed, ", "), SlashNoticeFeedback)
	}
	if send != nil {
		if status, statusErr := service.Status(ctx); statusErr == nil {
			sendStatusUpdate(send, status)
		}
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
	}
	return TaskResultMsg{SuppressTurnDivider: true, Err: controlprompt.FriendlyCommandError("disconnect", err)}
}
