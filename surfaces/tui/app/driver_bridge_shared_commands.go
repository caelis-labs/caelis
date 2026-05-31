package tuiapp

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

type sharedCommandOptions struct {
	ClearHistory  bool
	RefreshStatus bool
	Attachments   []Attachment
}

func slashSharedCommandWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), input string, opts sharedCommandOptions) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	input = strings.TrimSpace(input)
	if input == "" {
		return TaskResultMsg{SuppressTurnDivider: true}
	}
	view, err := driver.ExecuteCommand(ctx, tuidriver.CommandExecutionOptions{
		Input:       input,
		Attachments: convertAttachments(opts.Attachments),
	})
	if err != nil {
		return TaskResultMsg{Err: friendlyCommandError(strings.TrimPrefix(input, "/"), err)}
	}
	if opts.ClearHistory && send != nil {
		send(ClearHistoryMsg{})
	}
	if output := strings.TrimSpace(view.Output); output != "" {
		sendNotice(send, output)
	}
	if opts.RefreshStatus {
		refreshStatusViaSendWithContext(ctx, driver, send)
	}
	return TaskResultMsg{SuppressTurnDivider: true}
}
