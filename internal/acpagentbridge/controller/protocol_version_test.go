package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestStartACPClientRejectsV2BeforeSessionEffect(t *testing.T) {
	t.Parallel()
	for _, resumeID := range []string{"", "existing-session"} {
		t.Run("resume="+resumeID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			marker := filepath.Join(t.TempDir(), "session-called")
			cfg := steeringControllerTestConfig(`{"supported":true}`, marker)
			cfg.Env["CAELIS_ACP_TEST_PROTOCOL_VERSION"] = "2"
			connection, remoteID, _, err := (&Manager{}).startACPClient(ctx, t.TempDir(), cfg, resumeID, nil,
				func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
					t.Fatal("incompatible peer requested permission")
					return client.RequestPermissionResponse{}, nil
				})
			if err == nil || !strings.Contains(err.Error(), "requires stable v1") || connection != nil || remoteID != "" {
				t.Fatalf("start incompatible peer = %v, %q, %v", connection, remoteID, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("Session effect reached incompatible peer: %v", err)
			}
		})
	}
}
