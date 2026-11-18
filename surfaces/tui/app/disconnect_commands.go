package tuiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func slashDisconnectWithContext(ctx context.Context, service ControlServices, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	var targets []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &targets); err != nil || len(targets) == 0 {
		sendNotice(send, "Choose models in /disconnect", SlashNoticeHint)
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	completed, err := service.DeleteModels(ctx, targets)
	switch len(completed) {
	case 0:
	case 1:
		sendNotice(send, "Disconnected 1 model", SlashNoticeFeedback)
	default:
		sendNotice(send, fmt.Sprintf("Disconnected %d models", len(completed)), SlashNoticeFeedback)
	}
	if send != nil {
		send(statusRefreshRequestMsg{})
		if len(completed) > 0 {
			refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
		}
	}
	return TaskResultMsg{SuppressTurnDivider: true, Err: controlprompt.FriendlyCommandError("disconnect", err)}
}
