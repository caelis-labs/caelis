package controladapter

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestConnectImageInputCompletionAndSelectionPropagation(t *testing.T) {
	candidates, err := completeConnectArgs(context.Background(), nil, "connect-image-input:state", "", 10)
	if err != nil {
		t.Fatalf("completeConnectArgs(image input) error = %v", err)
	}
	if len(candidates) != 2 || candidates[0].Value != "false" || candidates[1].Value != "true" {
		t.Fatalf("image input candidates = %#v, want conservative false/true order", candidates)
	}

	enabled := true
	selections := connectModelSelections(controlprompt.ConnectConfig{
		Model:      "acme-vision",
		ImageInput: &enabled,
	})
	if len(selections) != 1 || selections[0].ImageInput == nil || !*selections[0].ImageInput {
		t.Fatalf("model selections = %#v, want explicit image input", selections)
	}
}
