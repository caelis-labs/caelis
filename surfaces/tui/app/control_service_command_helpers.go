package tuiapp

import "github.com/caelis-labs/caelis/internal/controlprompt"

func convertAttachments(items []Attachment) []controlprompt.Attachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]controlprompt.Attachment, len(items))
	for i, item := range items {
		out[i] = controlprompt.Attachment{
			Name:   item.Name,
			Offset: item.Offset,
		}
	}
	return out
}
