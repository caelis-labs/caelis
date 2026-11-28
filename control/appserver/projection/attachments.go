package projection

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const attachmentContentType = "caelis_session_image"

// AttachmentProjectionBytes leaves headroom for replacement and SSE framing.
const AttachmentProjectionBytes = 24 << 20

type attachmentContent struct {
	Type string              `json:"type"`
	Ref  model.AttachmentRef `json:"ref"`
	MIME string              `json:"mimeType"`
}

// AttachmentImageCount reports how many internal references a delivery carries.
func AttachmentImageCount(envelope eventstream.Envelope) int {
	update, ok := envelope.Update.(eventstream.ToolCallUpdate)
	if !ok {
		return 0
	}
	var count int
	for _, block := range update.Content {
		if _, ok := projectedAttachment(block.Content); ok {
			count++
		}
	}
	return count
}

// MaterializeAttachments translates internal spool content to existing ACP
// image or text blocks at an authorized Session or Task delivery boundary.
func MaterializeAttachments(ctx context.Context, reader model.AttachmentReader, sessionID string, envelope eventstream.Envelope, perImage int) (eventstream.Envelope, error) {
	if AttachmentImageCount(envelope) == 0 {
		return envelope, nil
	}
	update := envelope.Update.(eventstream.ToolCallUpdate)
	contents := slices.Clone(update.Content)
	for i, block := range update.Content {
		image, ok := projectedAttachment(block.Content)
		if !ok {
			continue
		}
		content := eventstream.TextContent{Type: "text", Text: "[Image attachment unavailable]"}
		contents[i].Content = content
		if reader == nil || perImage <= 0 || image.Ref.SessionID != sessionID {
			continue
		}
		data, err := model.LoadAttachment(ctx, reader, sessionID, image.Ref)
		if errors.Is(err, model.ErrAttachmentUnavailable) {
			continue
		}
		if err != nil {
			return envelope, err
		}
		var encoded string
		if base64.StdEncoding.EncodedLen(len(data)) > perImage {
			encoded, image.MIME, err = model.PreviewImage(data, perImage)
			if errors.Is(err, model.ErrAttachmentUnavailable) {
				continue
			}
			if err != nil {
				return envelope, err
			}
		} else {
			encoded = base64.StdEncoding.EncodeToString(data)
		}
		contents[i].Content = map[string]any{"type": "image", "mimeType": image.MIME, "data": encoded}
	}
	update.Content = contents
	envelope.Update = update
	return envelope, nil
}

func projectedAttachment(value any) (attachmentContent, bool) {
	if typed, ok := value.(attachmentContent); ok {
		return typed, true
	}
	raw, ok := value.(map[string]any)
	if !ok || raw["type"] != attachmentContentType {
		return attachmentContent{}, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return attachmentContent{}, false
	}
	var image attachmentContent
	err = json.Unmarshal(data, &image)
	return image, err == nil
}
