package controladapter

import (
	"context"
	"testing"
)

func TestConnectImageInputCompletion(t *testing.T) {
	candidates, err := completeConnectArgs(context.Background(), nil, "connect-image-input:state", "", 10)
	if err != nil {
		t.Fatalf("completeConnectArgs(image input) error = %v", err)
	}
	if len(candidates) != 2 || candidates[0].Value != "false" || candidates[1].Value != "true" {
		t.Fatalf("image input candidates = %#v, want conservative false/true order", candidates)
	}
}
