package tool

import (
	"context"
	"errors"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// ReferenceResultImages publishes native images in a Run's attachment store.
// A publication failure preserves other blocks and marks the image unavailable.
func ReferenceResultImages(ctx context.Context, result Result) (Result, error) {
	var content []ContentBlock
	var failure error
	for i, block := range result.Content {
		image, ok := block.(Image)
		if !ok || len(image.Data) == 0 {
			continue
		}
		ref, err := model.ReferenceImage(ctx, image.Data)
		if err != nil {
			if content == nil {
				content = slices.Clone(result.Content)
			}
			content[i] = Text("[Image attachment unavailable]")
			failure = errors.Join(failure, err)
			continue
		}
		if ref.Instance == "" {
			break
		}
		if content == nil {
			content = slices.Clone(result.Content)
		}
		image.Data, image.Attachment = nil, ref
		content[i] = image
	}
	if content != nil {
		result.Content = content
	}
	return result, failure
}
