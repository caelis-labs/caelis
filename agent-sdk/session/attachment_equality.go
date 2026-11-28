package session

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"reflect"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Acknowledging an idempotent retry must keep the original incarnation even
// after pruning. Wire spelling and every non-image field remain unchanged.
func sameEventAttachmentBytes(left, right *Event) bool {
	a, b := normalizeAttachmentBytes(left), normalizeAttachmentBytes(right)
	return (a || b) && reflect.DeepEqual(left, right)
}

func normalizeAttachmentBytes(event *Event) bool {
	referenced := false
	visit := func(message *model.Message) {
		if message == nil {
			return
		}
		_ = model.MapImageParts(message.Parts, func(m *model.MediaPart) error {
			switch m.Source.Kind {
			case model.MediaSourceInline:
				if raw, err := base64.StdEncoding.DecodeString(m.Source.Data); err == nil && len(raw) != 0 {
					m.Source = model.MediaSource{Kind: model.MediaSourceAttachment, Attachment: resultFingerprint(raw)}
				}
			case model.MediaSourceAttachment:
				referenced = true
				m.Source.Attachment = model.AttachmentRef{Digest: m.Source.Attachment.Digest, Size: m.Source.Attachment.Size}
			}
			return nil
		})
	}
	visit(event.Message)
	if event.InputEvidence != nil {
		visit(&event.InputEvidence.Message)
	}
	if event.Tool != nil && event.Tool.Result != nil {
		for i, block := range event.Tool.Result.Content {
			image, ok := block.(tool.Image)
			if !ok {
				continue
			}
			if image.Attachment.Instance != "" {
				referenced = true
				image.Attachment = model.AttachmentRef{Digest: image.Attachment.Digest, Size: image.Attachment.Size}
			} else if len(image.Data) != 0 {
				image.Attachment = resultFingerprint(image.Data)
				image.Data = nil
			}
			event.Tool.Result.Content[i] = image
		}
	}
	return referenced
}

func resultFingerprint(data []byte) model.AttachmentRef {
	digest := sha256.Sum256(data)
	return model.AttachmentRef{Digest: hex.EncodeToString(digest[:]), Size: int64(len(data))}
}
