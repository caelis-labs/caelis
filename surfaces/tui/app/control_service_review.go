package tuiapp

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
)

func slashReviewWithContext(ctx context.Context, service control.Service, sender *ProgramSender, args string, argsStart int, fullText string, attachments []Attachment) executeLineResult {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	instructions := strings.TrimSpace(args)
	promptAttachments := attachmentsForPromptRange(
		attachments,
		argsStart,
		len([]rune(strings.TrimSpace(fullText))),
	)
	turn, err := service.StartReviewSubagent(ctx, instructions, convertAttachments(promptAttachments))
	if err != nil {
		return executeLineResult{completion: TaskResultMsg{Err: friendlyCommandError("review", err)}}
	}
	return runSubagentTurn(ctx, service, sender, turn)
}
