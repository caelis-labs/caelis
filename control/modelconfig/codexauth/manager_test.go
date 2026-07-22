package codexauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
)

func TestHasCredentialsReloadsFileCreatedByAnotherManager(t *testing.T) {
	credentialPath := DefaultCredentialPath(t.TempDir())
	manager, err := NewManager(Options{CredentialPath: credentialPath})
	if err != nil {
		t.Fatal(err)
	}
	if manager.HasCredentials(context.Background()) {
		t.Fatal("HasCredentials() = true before credentials exist")
	}
	if err := writeStoredCredentials(credentialPath, storedCredentials{
		Version: credentialSchemaVersion, RefreshToken: "refresh", AccountID: "account",
	}); err != nil {
		t.Fatalf("writeStoredCredentials() error = %v", err)
	}
	if !manager.HasCredentials(context.Background()) {
		t.Fatal("HasCredentials() = false after another manager created credentials")
	}
}

func TestEnsureAuthenticatedCompletesPKCECallback(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	accountID := "account-from-id-token"
	var tokenCalls atomic.Int32
	var authorizationQuery url.Values
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenCalls.Add(1)
		if request.URL.Path != "/oauth/token" {
			http.NotFound(writer, request)
			return
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		if got := request.Form.Get("grant_type"); got != "authorization_code" {
			t.Errorf("grant_type = %q", got)
		}
		if got := request.Form.Get("client_id"); got != ClientID {
			t.Errorf("client_id = %q", got)
		}
		if got := request.Form.Get("redirect_uri"); got != RedirectURI {
			t.Errorf("redirect_uri = %q", got)
		}
		verifier := request.Form.Get("code_verifier")
		digest := sha256.Sum256([]byte(verifier))
		if got, want := base64.RawURLEncoding.EncodeToString(digest[:]), authorizationQuery.Get("code_challenge"); got != want {
			t.Errorf("PKCE challenge = %q, want %q", got, want)
		}
		writeJSON(t, writer, tokenResponse{
			AccessToken:  jwtForTest("", nil, now.Add(time.Hour)),
			RefreshToken: "refresh-login",
			IDToken:      jwtForTest(accountID, nil, time.Time{}),
		})
	}))
	defer issuer.Close()

	listener, callbackClient := newMemoryCallbackHarness(t)
	credentialPath := DefaultCredentialPath(t.TempDir())
	manager, err := NewManager(Options{
		HTTPClient:     issuer.Client(),
		Issuer:         issuer.URL,
		CredentialPath: credentialPath,
		Clock:          func() time.Time { return now },
		Headless:       func() bool { return false },
		Listen: func(string, string) (net.Listener, error) {
			return listener, nil
		},
		BrowserOpener: func(target string) error {
			parsed, err := url.Parse(target)
			if err != nil {
				return err
			}
			authorizationQuery = parsed.Query()
			if got := authorizationQuery.Get("client_id"); got != ClientID {
				t.Errorf("authorization client_id = %q", got)
			}
			if got := authorizationQuery.Get("redirect_uri"); got != RedirectURI {
				t.Errorf("authorization redirect_uri = %q", got)
			}
			if got := authorizationQuery.Get("code_challenge_method"); got != "S256" {
				t.Errorf("code_challenge_method = %q", got)
			}
			if got := authorizationQuery.Get("scope"); got != oauthScope {
				t.Errorf("scope = %q, want %q", got, oauthScope)
			}
			callback := "http://" + listener.Addr().String() + "/auth/callback?code=code-1&state=" + url.QueryEscape(authorizationQuery.Get("state"))
			response, err := callbackClient.Get(callback)
			if err != nil {
				return err
			}
			_ = response.Body.Close()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureAuthenticated(context.Background(), LoginOptions{OpenBrowser: true, CallbackTimeout: 2 * time.Second}); err != nil {
		t.Fatalf("EnsureAuthenticated() error = %v", err)
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("token calls = %d, want 1", tokenCalls.Load())
	}
	stored, err := readStoredCredentials(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "refresh-login" || stored.AccountID != accountID || stored.AccessToken == "" || stored.ExpiresAt <= now.Unix() {
		t.Fatalf("stored credentials = %#v", stored)
	}
	raw, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"access_token"`) || !strings.Contains(string(raw), `"expires_at"`) {
		t.Fatalf("credential file omitted shared-process access state")
	}
	reloaded, err := NewManager(Options{Issuer: issuer.URL, CredentialPath: credentialPath, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	var reloadedAuthorization string
	client, err := reloaded.AuthenticatedClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		reloadedAuthorization = request.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader("{}"))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if tokenCalls.Load() != 1 || !strings.HasPrefix(reloadedAuthorization, "Bearer ") {
		t.Fatalf("reloaded manager token calls = %d authorization=%q, want persisted access without refresh", tokenCalls.Load(), reloadedAuthorization)
	}
}

func TestEnsureAuthenticatedRejectsCallbackStateMismatch(t *testing.T) {
	var tokenCalls atomic.Int32
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		tokenCalls.Add(1)
	}))
	defer issuer.Close()
	listener, callbackClient := newMemoryCallbackHarness(t)
	manager, err := NewManager(Options{
		HTTPClient:     issuer.Client(),
		Issuer:         issuer.URL,
		CredentialPath: DefaultCredentialPath(t.TempDir()),
		Headless:       func() bool { return false },
		Listen: func(string, string) (net.Listener, error) {
			return listener, nil
		},
		BrowserOpener: func(string) error {
			response, err := callbackClient.Get("http://" + listener.Addr().String() + "/auth/callback?code=code-1&state=wrong")
			if err != nil {
				return err
			}
			_ = response.Body.Close()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.EnsureAuthenticated(context.Background(), LoginOptions{OpenBrowser: true, CallbackTimeout: 2 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("EnsureAuthenticated() error = %v, want state mismatch", err)
	}
	if tokenCalls.Load() != 0 {
		t.Fatalf("token calls = %d, want 0", tokenCalls.Load())
	}
}

func TestEnsureAuthenticatedDoesNotReplaceTransientRefreshFailureWithLogin(t *testing.T) {
	t.Parallel()

	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer issuer.Close()
	var browserCalls atomic.Int32
	manager, err := NewManager(Options{
		HTTPClient:     issuer.Client(),
		Issuer:         issuer.URL,
		CredentialPath: credentialPath,
		Headless:       func() bool { return false },
		BrowserOpener: func(string) error {
			browserCalls.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.EnsureAuthenticated(context.Background(), LoginOptions{OpenBrowser: true})
	if err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("EnsureAuthenticated() error = %v, want refresh failure", err)
	}
	if browserCalls.Load() != 0 {
		t.Fatalf("browser calls = %d, want 0 after transient refresh failure", browserCalls.Load())
	}
}

func TestEnsureAuthenticatedRequiresLoginWhenRefreshChangesAccount(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh", AccountID: "account-selected"}); err != nil {
		t.Fatal(err)
	}
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(t, writer, tokenResponse{
			AccessToken:  jwtForTest("account-other", nil, now.Add(time.Hour)),
			RefreshToken: "refresh-other",
		})
	}))
	defer issuer.Close()
	manager, err := NewManager(Options{
		HTTPClient:     issuer.Client(),
		Issuer:         issuer.URL,
		CredentialPath: credentialPath,
		Clock:          func() time.Time { return now },
		Headless:       func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.EnsureAuthenticated(context.Background(), LoginOptions{OpenBrowser: false})
	if !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("EnsureAuthenticated() error = %v, want reauthentication after account mismatch", err)
	}
	stored, err := readStoredCredentials(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccountID != "account-selected" || stored.RefreshToken != "refresh" {
		t.Fatalf("mismatched refresh changed selected account = %#v", stored)
	}
}

func TestMalformedCredentialsAreClassifiedForInteractiveRecovery(t *testing.T) {
	t.Parallel()

	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(credentialPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{
		CredentialPath: credentialPath,
		Headless:       func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.EnsureAuthenticated(context.Background(), LoginOptions{OpenBrowser: false})
	if !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("EnsureAuthenticated() error = %v, want recoverable malformed credential", err)
	}
}

func TestTokenExpiryHonorsExpiredJWTClaim(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	if got := tokenExpiry(jwtForTest("account", nil, expired), 3600, now); !got.Equal(expired) {
		t.Fatalf("tokenExpiry() = %s, want expired JWT claim %s", got, expired)
	}
}

func TestRefreshSingleflightRotatesCredentialAtomically(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{
		Version:      credentialSchemaVersion,
		RefreshToken: "refresh-old",
		AccountID:    "account-old",
	}); err != nil {
		t.Fatal(err)
	}
	var refreshCalls atomic.Int32
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		refreshCalls.Add(1)
		var body refreshRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode refresh request: %v", err)
		}
		if body.ClientID != ClientID || body.GrantType != "refresh_token" || body.RefreshToken != "refresh-old" {
			t.Errorf("refresh request = %#v", body)
		}
		writeJSON(t, writer, tokenResponse{
			AccessToken:  jwtForTest("account-old", nil, now.Add(time.Hour)),
			RefreshToken: "refresh-new",
		})
	}))
	defer issuer.Close()

	manager, err := NewManager(Options{
		HTTPClient:     issuer.Client(),
		Issuer:         issuer.URL,
		CredentialPath: credentialPath,
		Clock:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan http.Header, 32)
	base := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured <- request.Header.Clone()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}
	client, err := manager.AuthenticatedClient(base)
	if err != nil {
		t.Fatal(err)
	}
	const concurrentRequests = 24
	var wait sync.WaitGroup
	errorsSeen := make(chan error, concurrentRequests)
	for range concurrentRequests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader("{}"))
			if err != nil {
				errorsSeen <- err
				return
			}
			response, err := client.Do(request)
			if err == nil {
				_ = response.Body.Close()
			}
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("authenticated request error = %v", err)
		}
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls.Load())
	}
	close(captured)
	for headers := range captured {
		if got := headers.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("Authorization = %q", got)
		}
		if got := headers.Get("ChatGPT-Account-ID"); got != "account-old" {
			t.Errorf("ChatGPT-Account-ID = %q", got)
		}
	}
	stored, err := readStoredCredentials(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "refresh-new" || stored.AccountID != "account-old" {
		t.Fatalf("rotated credentials = %#v", stored)
	}
	assertCredentialPermissions(t, credentialPath)
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(credentialPath), ".auth.json.*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary credential files remain: %#v", temps)
	}
}

func TestRefreshCoordinatesManagersAndReusesPersistedAccess(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh-0", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	var refreshMu sync.Mutex
	currentRefresh := "refresh-0"
	refreshCalls := 0
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body refreshRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		refreshMu.Lock()
		defer refreshMu.Unlock()
		if body.RefreshToken != currentRefresh {
			writer.WriteHeader(http.StatusBadRequest)
			writeJSON(t, writer, tokenEndpointError{Error: "invalid_grant", ErrorDescription: "refresh token reused"})
			return
		}
		refreshCalls++
		currentRefresh = fmt.Sprintf("refresh-%d", refreshCalls)
		writeJSON(t, writer, tokenResponse{
			AccessToken:  jwtForTest("account", nil, now.Add(time.Hour)),
			RefreshToken: currentRefresh,
		})
	}))
	defer issuer.Close()

	base := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}
	clients := make([]*http.Client, 0, 2)
	for range 2 {
		manager, err := NewManager(Options{HTTPClient: issuer.Client(), Issuer: issuer.URL, CredentialPath: credentialPath, Clock: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		client, err := manager.AuthenticatedClient(base)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
	}
	start := make(chan struct{})
	errorsSeen := make(chan error, len(clients))
	var wait sync.WaitGroup
	for _, client := range clients {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			request, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader("{}"))
			response, err := client.Do(request)
			if response != nil {
				_ = response.Body.Close()
			}
			errorsSeen <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("shared credential request error = %v", err)
		}
	}
	refreshMu.Lock()
	gotRefreshCalls := refreshCalls
	refreshMu.Unlock()
	if gotRefreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want one serialized rotation shared by both managers", gotRefreshCalls)
	}
	stored, err := readStoredCredentials(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "refresh-1" || stored.AccountID != "account" || stored.AccessToken == "" {
		t.Fatalf("final shared credentials = %#v", stored)
	}
}

func TestAuthenticatedClientEnforcesAllowlistAndDoesNotMutateRequest(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{CredentialPath: credentialPath, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.loaded = true
	manager.stored = storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh", AccountID: "account"}
	manager.access = accessCredentials{token: "access-secret", accountID: "account", expiresAt: now.Add(time.Hour)}
	manager.mu.Unlock()
	var baseCalls atomic.Int32
	var received http.Header
	base := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		baseCalls.Add(1)
		received = request.Header.Clone()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}
	client, err := manager.AuthenticatedClient(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"http://chatgpt.com/backend-api/codex/responses",
		"https://evil.example/backend-api/codex/responses",
		"https://chatgpt.com:444/backend-api/codex/responses",
		"https://chatgpt.com/backend-api/codexevil/responses",
	} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		response, err := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "refusing to send OAuth credentials") {
			t.Errorf("Do(%q) error = %v, want allowlist rejection", target, err)
		}
	}
	if baseCalls.Load() != 0 {
		t.Fatalf("base transport calls after rejected requests = %d", baseCalls.Load())
	}
	request, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
	request.Header.Set("Authorization", "caller-value")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got := request.Header.Get("Authorization"); got != "caller-value" {
		t.Fatalf("original request Authorization mutated to %q", got)
	}
	if got := received.Get("Authorization"); got != "Bearer access-secret" {
		t.Fatalf("received Authorization = %q", got)
	}
	if got := received.Get("ChatGPT-Account-ID"); got != "account" {
		t.Fatalf("received ChatGPT-Account-ID = %q", got)
	}
}

func TestAuthenticatedClientInvalidatesUnauthorizedTokenWithoutRetryingRequest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh-old", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	var refreshCalls atomic.Int32
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		refreshCalls.Add(1)
		writeJSON(t, writer, tokenResponse{AccessToken: jwtForTest("account", nil, now.Add(time.Hour)), RefreshToken: "refresh-new"})
	}))
	defer issuer.Close()
	manager, err := NewManager(Options{HTTPClient: issuer.Client(), Issuer: issuer.URL, CredentialPath: credentialPath, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.loaded = true
	manager.stored = storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh-old", AccountID: "account"}
	manager.access = accessCredentials{token: "access-revoked", accountID: "account", expiresAt: now.Add(time.Hour)}
	manager.mu.Unlock()
	var backendCalls atomic.Int32
	var secondAuthorization string
	base := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := backendCalls.Add(1)
		if call == 1 {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unauthorized")), Request: request}, nil
		}
		secondAuthorization = request.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}
	client, err := manager.AuthenticatedClient(base)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader("{}"))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || backendCalls.Load() != 1 || refreshCalls.Load() != 0 {
		t.Fatalf("first request = status %d backend=%d refresh=%d, want terminal 401 without retry", response.StatusCode, backendCalls.Load(), refreshCalls.Load())
	}
	request, _ = http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader("{}"))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || backendCalls.Load() != 2 || refreshCalls.Load() != 1 || secondAuthorization == "Bearer access-revoked" {
		t.Fatalf("second request = status %d backend=%d refresh=%d auth=%q", response.StatusCode, backendCalls.Load(), refreshCalls.Load(), secondAuthorization)
	}
}

func TestExpiredRefreshIsTerminalThroughProviderRetryLayer(t *testing.T) {
	t.Parallel()

	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh-expired", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	var refreshCalls atomic.Int32
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		refreshCalls.Add(1)
		writer.WriteHeader(http.StatusBadRequest)
		writeJSON(t, writer, tokenEndpointError{Error: map[string]any{"code": "refresh_token_expired", "message": "refresh token expired"}})
	}))
	defer issuer.Close()
	manager, err := NewManager(Options{HTTPClient: issuer.Client(), Issuer: issuer.URL, CredentialPath: credentialPath})
	if err != nil {
		t.Fatal(err)
	}
	client, err := manager.AuthenticatedClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("backend request must not run without authentication")
	})})
	if err != nil {
		t.Fatal(err)
	}
	factory := providers.NewFactory()
	if err := factory.Register(providers.Config{
		Alias:      "codex",
		Provider:   "openai-codex",
		API:        providers.APIOpenAICodex,
		Model:      "gpt-test",
		BaseURL:    "https://chatgpt.com/backend-api/codex",
		HTTPClient: client,
		Auth:       providers.AuthConfig{Type: providers.AuthOAuthToken},
		Retry:      model.RetryConfig{MaxRetries: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond},
	}); err != nil {
		t.Fatal(err)
	}
	llm, err := factory.NewByAlias("codex")
	if err != nil {
		t.Fatal(err)
	}
	var generateErr error
	for _, err := range llm.Generate(context.Background(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")}}) {
		if err != nil {
			generateErr = err
			break
		}
	}
	if !errorcode.Is(generateErr, errorcode.Unauthenticated) || model.IsRetryableLLMError(generateErr) {
		t.Fatalf("Generate() error = %v code=%q retryable=%v", generateErr, errorcode.CodeOf(generateErr), model.IsRetryableLLMError(generateErr))
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want one terminal authentication attempt", refreshCalls.Load())
	}
}

func TestStoredCredentialPermissions(t *testing.T) {
	path := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(path, storedCredentials{Version: credentialSchemaVersion, RefreshToken: "refresh", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	assertCredentialPermissions(t, path)
}

func TestFirstAccountIDSupportsOrganizationFallback(t *testing.T) {
	if got := firstAccountID(jwtForTest("", []string{"org-account"}, time.Time{})); got != "org-account" {
		t.Fatalf("firstAccountID() = %q, want org-account", got)
	}
}

func assertCredentialPermissions(t *testing.T, path string) {
	t.Helper()
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential file mode = %04o, want 0600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("credential directory mode = %04o, want 0700", got)
	}
}

func jwtForTest(accountID string, organizations []string, expiry time.Time) string {
	claims := map[string]any{}
	if !expiry.IsZero() {
		claims["exp"] = expiry.Unix()
	}
	if accountID != "" {
		claims["https://api.openai.com/auth"] = map[string]any{"chatgpt_account_id": accountID}
	}
	if len(organizations) > 0 {
		items := make([]map[string]string, 0, len(organizations))
		for _, organization := range organizations {
			items = append(items, map[string]string{"id": organization})
		}
		claims["organizations"] = items
	}
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
