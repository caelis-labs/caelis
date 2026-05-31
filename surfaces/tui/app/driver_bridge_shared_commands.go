package tuiapp

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

type sharedCommandOptions struct {
	ClearHistory    bool
	RefreshStatus   bool
	RefreshCommands bool
	Attachments     []Attachment
	KeepTurnDivider bool
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
		if usage := commandUsageMessage(err); usage != "" {
			sendNotice(send, usage)
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		return TaskResultMsg{Err: friendlyCommandError(strings.TrimPrefix(input, "/"), err)}
	}
	if opts.ClearHistory && send != nil {
		send(ClearHistoryMsg{})
	}
	if output := strings.TrimSpace(view.Output); output != "" {
		sendNotice(send, output)
	}
	sendSharedCommandEvents(send, view.Events)
	if opts.RefreshStatus {
		refreshStatusViaSendWithContext(ctx, driver, send)
	}
	if opts.RefreshCommands {
		refreshAgentSlashCommandsViaSendWithContext(ctx, driver, send)
	}
	return TaskResultMsg{SuppressTurnDivider: !opts.KeepTurnDivider}
}

func commandUsageMessage(err error) string {
	if err == nil {
		return ""
	}
	raw := strings.TrimSpace(err.Error())
	if raw == "" {
		return ""
	}
	idx := strings.Index(strings.ToLower(raw), "usage:")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(raw[idx:])
}

func sendSharedCommandEvents(send func(tea.Msg), events []session.Event) {
	if send == nil || len(events) == 0 {
		return
	}
	for _, event := range events {
		if event.Type == "" {
			continue
		}
		if transcriptEvents := ProjectCoreSessionEventToTranscriptEvents(event); len(transcriptEvents) > 0 {
			send(TranscriptEventsMsg{Events: transcriptEvents})
		}
	}
}
