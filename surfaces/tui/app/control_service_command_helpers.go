package tuiapp

import (
	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	controlcommands "github.com/OnslaughtSnail/caelis/protocol/acp/control/commands"
)

func parseConnectArgs(args string) control.ConnectConfig {
	return controlcommands.ParseConnectArgs(args)
}

func modeToggleHint(status control.StatusSnapshot) string {
	return controlcommands.ModeToggleHint(status)
}

func friendlyCommandError(action string, err error) error {
	return controlcommands.FriendlyCommandError(action, err)
}

func convertAttachments(items []Attachment) []control.Attachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]control.Attachment, len(items))
	for i, item := range items {
		out[i] = control.Attachment{
			Name:   item.Name,
			Offset: item.Offset,
		}
	}
	return out
}
