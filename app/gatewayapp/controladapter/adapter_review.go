package controladapter

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
)

func (d *Adapter) StartReview(ctx context.Context, instructions string, attachments []Attachment) (Turn, error) {
	prompt, attachmentOffset := gatewayapp.ReviewPrompt(instructions)
	return d.startSidecarTurn(ctx, startSidecarTurnRequest{
		Agent:        gatewayapp.ReviewerAgentID,
		LabelBase:    gatewayapp.ReviewerAgentID,
		Prompt:       prompt,
		DisplayInput: displayInputWithAttachments(instructions, attachments),
		DisplayTitle: reviewDisplayTitle(instructions),
		Attachments:  shiftControlAttachments(attachments, attachmentOffset),
		Source:       "slash_review",
		Transient:    true,
	})
}

func reviewDisplayTitle(instructions string) string {
	if strings.TrimSpace(instructions) != "" {
		return ""
	}
	return "Code review requested"
}

func shiftControlAttachments(items []Attachment, offset int) []Attachment {
	if len(items) == 0 || offset == 0 {
		return append([]Attachment(nil), items...)
	}
	out := make([]Attachment, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		data := strings.TrimSpace(item.Data)
		if name == "" && data == "" {
			continue
		}
		out = append(out, Attachment{
			Name:     name,
			Offset:   max(item.Offset, 0) + offset,
			MimeType: strings.TrimSpace(item.MimeType),
			Data:     data,
		})
	}
	return out
}
