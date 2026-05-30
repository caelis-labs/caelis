package codefree

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureAuthUsesExistingCredentials(t *testing.T) {
	credsPath := writeCredentials(t, "272182", "live-api-key")
	result, err := EnsureAuth(context.Background(), AuthOptions{CredentialPath: credsPath})
	if err != nil {
		t.Fatal(err)
	}
	if result.CredentialPath != credsPath || result.UserID != "272182" {
		t.Fatalf("result = %#v, want existing credential result", result)
	}
}

func TestEnsureModelSelectionAuthRunsOAuthAndStoresCredentials(t *testing.T) {
	ctx := context.Background()
	credsPath := filepath.Join(t.TempDir(), "oauth_creds.json")
	var sawCode string
	var sawVerifier string
	var sawAPIKeySession string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case oauthTokenPath:
			sawCode = r.URL.Query().Get("code")
			sawVerifier = r.URL.Query().Get("code_verifier")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id_token":                 "user-1",
				"ori_session_id":           "session-1",
				"refresh_token":            "refresh-1",
				"expires_in":               3600,
				"refresh_token_expires_in": 7200,
			})
		case userAPIKeyPath:
			sawAPIKeySession = r.Header.Get("sessionId")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"encryptedApiKey": encryptAPIKeyForTest(t, "oauth-api-key"),
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	started, err := EnsureModelSelectionAuth(ctx, AuthOptions{
		BaseURL:         server.URL,
		CredentialPath:  credsPath,
		CallbackTimeout: 5 * time.Second,
		NotifyAuthURL: func(rawURL string) {
			go completeCodeFreeOAuthCallback(t, rawURL, "auth-code")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("started = false, want OAuth login to start")
	}
	if sawCode != "auth-code" || sawVerifier == "" || sawAPIKeySession != "session-1" {
		t.Fatalf("oauth exchange code=%q verifier=%q session=%q", sawCode, sawVerifier, sawAPIKeySession)
	}
	creds, err := loadCredentials(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if creds.UserID != "user-1" || creds.APIKey != "oauth-api-key" {
		t.Fatalf("stored creds = %#v, want OAuth credentials", creds)
	}
	stored, err := readStoredCredentialsAtPath(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Cached.RefreshToken != "refresh-1" || stored.Cached.BaseURL != server.URL {
		t.Fatalf("stored cached = %#v, want refresh token and base URL", stored.Cached)
	}
}

func TestRefreshUpdatesStoredCodeFreeCredentials(t *testing.T) {
	ctx := context.Background()
	credsPath := writeRefreshableCredentials(t, "user-old", "old-api-key", "refresh-old")
	var refreshGrant string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case oauthTokenPath:
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			refreshGrant = r.Form.Get("grant_type")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id_token":       "user-new",
				"ori_session_id": "session-new",
				"refresh_token":  "refresh-new",
				"expires_in":     3600,
			})
		case userAPIKeyPath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"encryptedApiKey": encryptAPIKeyForTest(t, "new-api-key"),
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := Refresh(ctx, RefreshOptions{BaseURL: server.URL, CredentialPath: credsPath})
	if err != nil {
		t.Fatal(err)
	}
	if refreshGrant != "refresh_token" || result.UserID != "user-new" || !result.HasRefreshToken {
		t.Fatalf("refresh grant=%q result=%#v, want refreshed credentials", refreshGrant, result)
	}
	creds, err := loadCredentials(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if creds.UserID != "user-new" || creds.APIKey != "new-api-key" {
		t.Fatalf("credentials = %#v, want refreshed API key", creds)
	}
}

func completeCodeFreeOAuthCallback(t *testing.T, rawURL string, code string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Errorf("parse auth url: %v", err)
		return
	}
	state := parsed.Query().Get("state")
	decoded, err := base64.StdEncoding.DecodeString(state)
	if err != nil {
		t.Errorf("decode state: %v", err)
		return
	}
	callbackURL, err := url.Parse(string(decoded))
	if err != nil {
		t.Errorf("parse callback url: %v", err)
		return
	}
	values := callbackURL.Query()
	values.Set("code", code)
	values.Set("state", state)
	callbackURL.RawQuery = values.Encode()
	resp, err := http.Get(callbackURL.String())
	if err != nil {
		t.Errorf("call callback: %v", err)
		return
	}
	_ = resp.Body.Close()
}

func writeRefreshableCredentials(t *testing.T, userID string, apiKey string, refreshToken string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oauth_creds.json")
	raw, err := json.Marshal(cachedCredentials{
		UserID:              userID,
		APIKey:              encryptAPIKeyForTest(t, apiKey),
		RefreshToken:        refreshToken,
		ExpiresAtUnixMilli:  time.Now().Add(-time.Hour).UnixMilli(),
		ObtainedAtUnixMilli: time.Now().Add(-2 * time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
