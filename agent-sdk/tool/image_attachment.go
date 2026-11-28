package tool

import (
	"context"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// ReferenceResultImages publishes native images in a Run's attachment store.
// The caller receives the original result intact if any publication fails.
func ReferenceResultImages(ctx context.Context, result Result) (Result, error) {
	var content []ContentBlock
	for i, block := range result.Content {
		image, ok := block.(Image)
		if !ok || len(image.Data) == 0 {
			continue
		}
		ref, err := model.ReferenceImage(ctx, image.Data)
		if err != nil {
			return result, err
		}
		if ref.Instance == "" {
			return result, nil
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
	return result, nil
}
