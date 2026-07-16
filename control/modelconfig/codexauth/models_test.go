package codexauth

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestListModelsUsesAccountCatalogVisibilityWithoutAPISupportFilter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(Options{
		CredentialPath: filepath.Join(t.TempDir(), "auth.json"),
		Clock:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.loaded = true
	manager.stored = storedCredentials{
		Version:      credentialSchemaVersion,
		RefreshToken: "refresh",
		AccountID:    "account-pro",
	}
	manager.access = accessCredentials{
		token:     "access",
		accountID: "account-pro",
		expiresAt: now.Add(time.Hour),
	}

	base := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != codexModelsURL+"?client_version="+codexModelsClientVersion {
			t.Errorf("models request = %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer access" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("ChatGPT-Account-ID"); got != "account-pro" {
			t.Errorf("ChatGPT-Account-ID = %q", got)
		}
		if got := request.Header.Get("originator"); got != "caelis" {
			t.Errorf("originator = %q", got)
		}
		body := `{"models":[
			{"slug":"gpt-5.4","visibility":"list","priority":16,"supported_in_api":true},
			{"slug":"gpt-5.6-sol","visibility":"list","priority":1,"supported_in_api":true},
			{"slug":"gpt-5.3-codex-spark","visibility":"list","priority":26,"supported_in_api":false},
			{"slug":"codex-auto-review","visibility":"hide","priority":43,"supported_in_api":true},
			{"slug":"gpt-5.4","visibility":"list","priority":17,"supported_in_api":true}
		]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}

	models, err := manager.ListModels(context.Background(), base)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	want := []string{"gpt-5.6-sol", "gpt-5.4", "gpt-5.3-codex-spark"}
	if !slices.Equal(models, want) {
		t.Fatalf("ListModels() = %#v, want %#v", models, want)
	}
}

func TestListModelsUsesCodexCompatibilityVersion(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(Options{
		CredentialPath: filepath.Join(t.TempDir(), "auth.json"),
		Clock:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.loaded = true
	manager.stored = storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh", AccountID: "account"}
	manager.access = accessCredentials{token: "access", accountID: "account", expiresAt: now.Add(time.Hour)}

	base := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.URL.Query().Get("client_version"); got != codexModelsClientVersion {
			t.Errorf("client_version = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"models":[]}`)),
			Request:    request,
		}, nil
	})}

	models, err := manager.ListModels(context.Background(), base)
	if err != nil || len(models) != 0 {
		t.Fatalf("ListModels() = %#v, %v", models, err)
	}
}
