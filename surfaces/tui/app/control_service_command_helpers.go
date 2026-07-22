package tuiapp

import "github.com/caelis-labs/caelis/protocol/acp/control"

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
