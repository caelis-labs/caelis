package gatewayapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
)

func TestAuthenticateModelProviderUsesStoredCodexCredential(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	credentialPath := codexauth.DefaultCredentialPath(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(credentialPath), 0o700); err != nil {
		t.Fatal(err)
	}
	credentials := fmt.Sprintf(
		"{\"version\":1,\"refresh_token\":\"refresh\",\"account_id\":\"account\",\"access_token\":\"access\",\"expires_at\":%d}\n",
		now.Add(time.Hour).Unix(),
	)
	if err := os.WriteFile(credentialPath, []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := codexauth.NewManager(codexauth.Options{
		CredentialPath: credentialPath,
		Clock:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = (&Stack{codexAuth: manager}).authenticateModelProvider(context.Background(), modelconfig.AuthenticateRequest{
		Provider: "openai-codex",
	})
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
}
