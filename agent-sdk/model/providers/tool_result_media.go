package providers

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// inlineToolResultImages returns the image payloads embedded in canonical tool
// results. Provider adapters decide how their wire dialect carries them.
func inlineToolResultImages(message model.Message) []model.MediaPart {
	var images []model.MediaPart
	for _, result := range message.ToolResults() {
		for _, part := range result.Content {
			if part.Kind != model.PartKindMedia || part.Media == nil {
				continue
			}
			media := *part.Media
			if media.Modality != model.MediaModalityImage ||
				media.Source.Kind != model.MediaSourceInline ||
				strings.TrimSpace(media.Source.Data) == "" ||
				strings.TrimSpace(media.MimeType) == "" {
				continue
			}
			images = append(images, media)
		}
	}
	return images
}
