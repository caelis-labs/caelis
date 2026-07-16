package gatewayapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
)

func TestModelServiceCodexEmptyAccountCatalogIsAuthoritative(t *testing.T) {
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
	client := &http.Client{Transport: gatewayRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"models":[]}`)),
			Request:    request,
		}, nil
	})}
	result, err := (ModelService{stack: &Stack{codexAuth: manager}}).Authenticate(context.Background(), modelconfig.AuthenticateRequest{
		Provider:   "openai-codex",
		Purpose:    modelconfig.AuthPurposeModelSelection,
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if !result.ModelCatalogAuthoritative || len(result.SelectableModels) != 0 {
		t.Fatalf("Authenticate() = %#v, want authoritative empty account catalog", result)
	}
}
