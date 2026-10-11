package codex

import (
	"errors"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func TestInitializeRejectsInvalidVersionBeforeBackendAccess(t *testing.T) {
	t.Parallel()
	for _, version := range []acp.ProtocolVersion{0} {
		// No backend or connection is configured: invalid protocol input
		// must be rejected before account reads or other backend activity.
		a := &agent{}
		_, err := a.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: version})
		var requestError *acp.RequestError
		if !errors.As(err, &requestError) || requestError.Code != -32602 {
			t.Fatalf("initialize version %d = %v, want invalid params", version, err)
		}
	}
}
