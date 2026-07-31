package controladapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (d *assembler) StartReview(ctx context.Context, instructions string, attachments []controlprompt.Attachment) (controlprompt.Turn, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.ResolveFn == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: system Agent placement is unavailable")
	}
	placement, err := d.stack.AgentBinding.ResolveFn(bindingContext(ctx), agentbinding.HandleReviewer)
	if err != nil {
		return nil, err
	}
	prompt, attachmentOffset := gatewayapp.ReviewPrompt(instructions)
	return d.startSidecarTurn(ctx, startSidecarTurnRequest{
		Agent:          gatewayapp.ReviewerAgentID,
		Placement:      placement,
		LabelBase:      gatewayapp.ReviewerAgentID,
		Prompt:         prompt,
		DisplayInput:   displayInputWithAttachments(instructions, attachments),
		DisplayAddress: "/review",
		DisplayTitle:   reviewDisplayTitle(instructions),
		Attachments:    shiftControlAttachments(attachments, attachmentOffset),
		Source:         "slash_review",
		Transient:      true,
	})
}

func reviewDisplayTitle(instructions string) string {
	if strings.TrimSpace(instructions) != "" {
		return ""
	}
	return "Code review requested"
}

func shiftControlAttachments(items []controlprompt.Attachment, offset int) []controlprompt.Attachment {
	if len(items) == 0 || offset == 0 {
		return append([]controlprompt.Attachment(nil), items...)
	}
	out := make([]controlprompt.Attachment, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		data := strings.TrimSpace(item.Data)
		if name == "" && data == "" {
			continue
		}
		out = append(out, controlprompt.Attachment{
			Name:     name,
			Offset:   max(item.Offset, 0) + offset,
			MimeType: strings.TrimSpace(item.MimeType),
			Data:     data,
		})
	}
	return out
}
