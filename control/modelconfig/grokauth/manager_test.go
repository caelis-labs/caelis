package grokauth

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
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestEnsureAuthenticatedCompletesPKCECallback(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	var authorizationQuery url.Values
	var tokenCalls atomic.Int32
	issuerClient := newInMemoryHTTPClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenCalls.Add(1)
		if request.URL.Path != "/oauth2/token" {
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
		if got, want := request.Form.Get("redirect_uri"), authorizationQuery.Get("redirect_uri"); got != want {
			t.Errorf("redirect_uri = %q, want %q", got, want)
		}
		verifier := request.Form.Get("code_verifier")
		digest := sha256.Sum256([]byte(verifier))
		if got, want := base64.RawURLEncoding.EncodeToString(digest[:]), authorizationQuery.Get("code_challenge"); got != want {
			t.Errorf("PKCE challenge = %q, want %q", got, want)
		}
		writeJSON(t, writer, tokenResponse{AccessToken: "access-login", RefreshToken: "refresh-login", ExpiresIn: 3600})
	}))
	listener, callbackClient := newMemoryCallbackHarnessAt(t, "127.0.0.1:43123")
	credentialPath := DefaultCredentialPath(t.TempDir())
	manager, err := NewManager(Options{
		HTTPClient:     issuerClient,
		Issuer:         "https://auth.x.ai",
		CredentialPath: credentialPath,
		Clock:          func() time.Time { return now },
		Headless:       func() bool { return false },
		Listen: func(network string, address string) (net.Listener, error) {
			if network != "tcp" || address != "127.0.0.1:0" {
				t.Errorf("Listen() = %q, %q", network, address)
			}
			return listener, nil
		},
		BrowserOpener: func(target string) error {
			parsed, err := url.Parse(target)
			if err != nil {
				return err
			}
			authorizationQuery = parsed.Query()
			for key, want := range map[string]string{
				"client_id":             ClientID,
				"scope":                 oauthScope,
				"code_challenge_method": "S256",
				"plan":                  "generic",
				"referrer":              "caelis",
			} {
				if got := authorizationQuery.Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			if authorizationQuery.Get("nonce") == "" || authorizationQuery.Get("state") == "" {
				t.Error("authorization URL omitted nonce or state")
			}
			if got := authorizationQuery.Get("redirect_uri"); got != "http://127.0.0.1:43123/callback" {
				t.Errorf("redirect_uri = %q, want dynamic listener port", got)
			}
			callback := "http://" + listener.Addr().String() + "/callback?code=code-1&state=" + url.QueryEscape(authorizationQuery.Get("state"))
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
	inputStarted := make(chan struct{})
	inputCanceled := make(chan struct{})
	ctx := modelconfig.WithAuthInput(context.Background(), func(inputCtx context.Context, _ modelconfig.AuthInputRequest) (string, error) {
		close(inputStarted)
		<-inputCtx.Done()
		close(inputCanceled)
		return "", inputCtx.Err()
	})
	if err := manager.EnsureAuthenticated(ctx, LoginOptions{OpenBrowser: true, CallbackTimeout: 2 * time.Second}); err != nil {
		t.Fatalf("EnsureAuthenticated() error = %v", err)
	}
	select {
	case <-inputStarted:
	case <-time.After(time.Second):
		t.Fatal("manual input race did not start")
	}
	select {
	case <-inputCanceled:
	case <-time.After(time.Second):
		t.Fatal("loopback completion did not cancel manual input")
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("token calls = %d, want 1", tokenCalls.Load())
	}
	stored, err := readStoredCredentials(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "access-login" || stored.RefreshToken != "refresh-login" || stored.ExpiresAt != now.Add(time.Hour).Unix() {
		t.Fatalf("stored credentials = %#v", stored)
	}
	info, err := os.Stat(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o, want 600", info.Mode().Perm())
	}
}

func TestEnsureAuthenticatedAcceptsPastedAuthorizationCode(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	listener, callbackClient := newMemoryCallbackHarnessAt(t, "127.0.0.1:43124")
	var authorizationQuery url.Values
	client := newInMemoryHTTPClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/oauth2/token" {
			http.NotFound(writer, request)
			return
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		if got := request.Form.Get("code"); got != "pasted-code" {
			t.Errorf("code = %q", got)
		}
		if got, want := request.Form.Get("redirect_uri"), authorizationQuery.Get("redirect_uri"); got != want {
			t.Errorf("redirect_uri = %q, want %q", got, want)
		}
		writeJSON(t, writer, tokenResponse{AccessToken: "access-paste", RefreshToken: "refresh-paste", ExpiresIn: 3600})
	}))
	manager, err := NewManager(Options{
		HTTPClient:     client,
		Issuer:         "https://auth.x.ai",
		CredentialPath: DefaultCredentialPath(t.TempDir()),
		Clock:          func() time.Time { return now },
		Headless:       func() bool { return false },
		Listen: func(network string, address string) (net.Listener, error) {
			if network != "tcp" || address != "127.0.0.1:0" {
				t.Errorf("Listen() = %q, %q", network, address)
			}
			return listener, nil
		},
		BrowserOpener: func(target string) error {
			parsed, err := url.Parse(target)
			if err != nil {
				return err
			}
			authorizationQuery = parsed.Query()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var inputRequest modelconfig.AuthInputRequest
	ctx := modelconfig.WithAuthInput(context.Background(), func(_ context.Context, request modelconfig.AuthInputRequest) (string, error) {
		inputRequest = request
		return "pasted-code", nil
	})
	if err := manager.EnsureAuthenticated(ctx, LoginOptions{OpenBrowser: true, CallbackTimeout: time.Second}); err != nil {
		t.Fatalf("EnsureAuthenticated() error = %v", err)
	}
	if inputRequest.Provider != "xai" || inputRequest.Prompt == "" || !inputRequest.Secret {
		t.Fatalf("auth input request = %#v", inputRequest)
	}
	response, err := callbackClient.Get("http://" + listener.Addr().String() + "/callback?code=late&state=late")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("manual paste completion left the loopback callback server running")
	}
}

func TestCallbackServerHandlesAccountsAppPrivateNetworkPreflight(t *testing.T) {
	t.Parallel()

	results := make(chan callbackResult, 1)
	server := callbackServer("expected-state", results)
	preflight := httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:12345/callback", nil)
	preflight.Header.Set("Origin", accountsAppOrigin)
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	preflight.Header.Set("Access-Control-Request-Private-Network", "true")
	recorder := httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, preflight)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	for header, want := range map[string]string{
		"Access-Control-Allow-Origin":          accountsAppOrigin,
		"Access-Control-Allow-Methods":         http.MethodGet,
		"Access-Control-Allow-Private-Network": "true",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	select {
	case result := <-results:
		t.Fatalf("preflight consumed callback result: %#v", result)
	default:
	}

	invalidPreflight := httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:12345/callback", nil)
	invalidPreflight.Header.Set("Origin", accountsAppOrigin)
	invalidPreflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	recorder = httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, invalidPreflight)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("invalid preflight status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	post := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:12345/callback?code=code-1&state=expected-state", nil)
	post.Header.Set("Origin", accountsAppOrigin)
	recorder = httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, post)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	select {
	case result := <-results:
		t.Fatalf("invalid callback request consumed result: %#v", result)
	default:
	}

	for _, rawURL := range []string{
		"http://127.0.0.1:12345/callback?code=stale-code&state=wrong-state",
		"http://127.0.0.1:12345/callback?state=expected-state",
	} {
		invalid := httptest.NewRequest(http.MethodGet, rawURL, nil)
		invalid.Header.Set("Origin", accountsAppOrigin)
		recorder = httptest.NewRecorder()
		server.Handler.ServeHTTP(recorder, invalid)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid callback %q status = %d, want %d", rawURL, recorder.Code, http.StatusBadRequest)
		}
		select {
		case result := <-results:
			t.Fatalf("invalid callback %q consumed result: %#v", rawURL, result)
		default:
		}
	}

	callback := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:12345/callback?code=code-1&state=expected-state", nil)
	callback.Header.Set("Origin", accountsAppOrigin)
	recorder = httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, callback)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Access-Control-Allow-Origin") != accountsAppOrigin {
		t.Fatalf("callback response = %d, headers %#v", recorder.Code, recorder.Header())
	}
	select {
	case result := <-results:
		if result.err != nil || result.code != "code-1" {
			t.Fatalf("callback result = %#v", result)
		}
	default:
		t.Fatal("callback GET did not publish a result")
	}
}

func TestCallbackServerCompletesPrivateNetworkFetchOverRealLoopback(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("real loopback socket is unavailable in this environment: %v", err)
	}
	redirectURI, err := loopbackRedirectURI(listener)
	if err != nil || strings.Contains(redirectURI, ":0/") {
		t.Fatalf("loopbackRedirectURI() = %q, %v", redirectURI, err)
	}
	results := make(chan callbackResult, 1)
	server := callbackServer("expected-state", results)
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-serveDone
	})

	client := &http.Client{
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		Timeout:   time.Second,
	}
	callbackURL := "http://" + listener.Addr().String() + "/callback"
	preflight, err := http.NewRequest(http.MethodOptions, callbackURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight.Header.Set("Origin", accountsAppOrigin)
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	preflight.Header.Set("Access-Control-Request-Private-Network", "true")
	response, err := client.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.Header.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("preflight response = %d, headers %#v", response.StatusCode, response.Header)
	}

	callback, err := http.NewRequest(http.MethodGet, callbackURL+"?code=real-loopback-code&state=expected-state", nil)
	if err != nil {
		t.Fatal(err)
	}
	callback.Header.Set("Origin", accountsAppOrigin)
	response, err = client.Do(callback)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
	select {
	case result := <-results:
		if result.err != nil || result.code != "real-loopback-code" {
			t.Fatalf("callback result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("real loopback callback did not reach the OAuth result channel")
	}
}

func TestCallbackServerRejectsUntrustedCrossOriginRequestWithoutConsumingResult(t *testing.T) {
	t.Parallel()

	results := make(chan callbackResult, 1)
	server := callbackServer("expected-state", results)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:12345/callback?code=stolen&state=expected-state", nil)
	request.Header.Set("Origin", "https://example.com")
	recorder := httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	select {
	case result := <-results:
		t.Fatalf("untrusted origin consumed callback result: %#v", result)
	default:
	}
}

func TestParsePastedCallback(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
		code  string
		err   string
	}{
		{name: "bare code", input: " abc123 ", code: "abc123"},
		{name: "callback URL", input: "http://127.0.0.1:54321/callback?code=abc123&state=expected", code: "abc123"},
		{name: "callback URL state mismatch", input: "http://127.0.0.1:54321/callback?code=abc123&state=wrong", err: "state mismatch"},
		{name: "callback URL error", input: "http://127.0.0.1:54321/callback?error=access_denied&error_description=User+denied", err: "User denied"},
		{name: "callback URL missing code", input: "http://127.0.0.1:54321/callback", err: "omitted authorization code"},
		{name: "empty", input: " ", err: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := parsePastedCallback(tc.input, "expected")
			if tc.err != "" {
				if result.err == nil || !strings.Contains(result.err.Error(), tc.err) {
					t.Fatalf("parsePastedCallback() = %#v, want error %q", result, tc.err)
				}
				return
			}
			if result.err != nil || result.code != tc.code {
				t.Fatalf("parsePastedCallback() = %#v, want code %q", result, tc.code)
			}
		})
	}
}

func TestDeviceCodeHandlesPendingSlowDownAndReportsProgress(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var polls int
	var sleeps []time.Duration
	client := newInMemoryHTTPClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		switch request.URL.Path {
		case "/oauth2/device/code":
			if request.Form.Get("client_id") != ClientID || request.Form.Get("scope") != oauthScope {
				t.Errorf("device form = %v", request.Form)
			}
			writeJSON(t, writer, deviceCodeResponse{
				DeviceCode: "device-1", UserCode: "ABCD-EFGH",
				VerificationURI: "https://auth.x.ai/activate", ExpiresIn: 60, Interval: 1,
			})
		case "/oauth2/token":
			mu.Lock()
			polls++
			poll := polls
			mu.Unlock()
			if request.Form.Get("grant_type") != deviceGrantType || request.Form.Get("device_code") != "device-1" {
				t.Errorf("poll form = %v", request.Form)
			}
			switch poll {
			case 1:
				writer.WriteHeader(http.StatusBadRequest)
				writeJSON(t, writer, tokenEndpointError{Error: "authorization_pending"})
			case 2:
				writer.WriteHeader(http.StatusBadRequest)
				writeJSON(t, writer, tokenEndpointError{Error: "slow_down"})
			default:
				writeJSON(t, writer, tokenResponse{AccessToken: "device-access", RefreshToken: "device-refresh", ExpiresIn: 3600})
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	manager, err := NewManager(Options{
		HTTPClient:     client,
		Issuer:         "https://auth.x.ai",
		CredentialPath: DefaultCredentialPath(t.TempDir()),
		Clock:          func() time.Time { return now },
		Sleep: func(_ context.Context, duration time.Duration) error {
			mu.Lock()
			sleeps = append(sleeps, duration)
			now = now.Add(duration)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var progress []modelconfig.AuthProgress
	ctx := modelconfig.WithAuthProgress(context.Background(), func(update modelconfig.AuthProgress) {
		progress = append(progress, update)
	})
	if err := manager.EnsureAuthenticated(ctx, LoginOptions{DeviceCode: true}); err != nil {
		t.Fatalf("EnsureAuthenticated() error = %v", err)
	}
	if polls != 3 {
		t.Fatalf("polls = %d, want 3", polls)
	}
	if len(sleeps) != 2 || sleeps[0] != time.Second || sleeps[1] != 6*time.Second {
		t.Fatalf("sleeps = %v, want [1s 6s]", sleeps)
	}
	if len(progress) != 3 || progress[1].VerificationURL != "https://auth.x.ai/activate" || progress[1].UserCode != "ABCD-EFGH" {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestDeviceCodeTerminalErrors(t *testing.T) {
	for _, tc := range []struct {
		code string
		want string
	}{
		{code: "access_denied", want: "was denied"},
		{code: "expired_token", want: "expired"},
		{code: "server_error", want: "server_error"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
			client := newInMemoryHTTPClient(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusBadRequest)
				writeJSON(t, writer, tokenEndpointError{Error: tc.code})
			}))
			manager, err := NewManager(Options{
				CredentialPath: DefaultCredentialPath(t.TempDir()),
				Clock:          func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = manager.pollDeviceCode(context.Background(), client, deviceCodeResponse{
				DeviceCode: "device", ExpiresIn: 60, Interval: 1,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pollDeviceCode() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAuthenticatedClientRefreshesOnceAndPersistsRotatedToken(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{
		Version: credentialSchemaVersion, RefreshToken: "refresh-old", AccessToken: "expired", ExpiresAt: now.Add(-time.Minute).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	var refreshCalls atomic.Int32
	var apiCalls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "auth.x.ai":
			refreshCalls.Add(1)
			if err := request.ParseForm(); err != nil {
				t.Errorf("ParseForm() error = %v", err)
			}
			if request.Form.Get("refresh_token") != "refresh-old" {
				t.Errorf("refresh_token = %q", request.Form.Get("refresh_token"))
			}
			return jsonResponse(request, http.StatusOK, tokenResponse{
				AccessToken: "access-new", RefreshToken: "refresh-new", ExpiresIn: 3600,
			}), nil
		case "cli-chat-proxy.grok.com":
			apiCalls.Add(1)
			if got := request.Header.Get("Authorization"); got != "Bearer access-new" {
				t.Errorf("Authorization = %q", got)
			}
			if got := request.Header.Get("X-XAI-Token-Auth"); got != "xai-grok-cli" {
				t.Errorf("X-XAI-Token-Auth = %q", got)
			}
			if got := request.Header.Get("x-grok-client-version"); got != grokBuildProtocolVersion {
				t.Errorf("x-grok-client-version = %q", got)
			}
			if got := request.Header.Get("x-grok-client-identifier"); got != "caelis" {
				t.Errorf("x-grok-client-identifier = %q", got)
			}
			return textResponse(request, http.StatusOK, "ok"), nil
		default:
			return nil, fmt.Errorf("unexpected host %q", request.URL.Host)
		}
	})
	base := &http.Client{Transport: transport}
	manager, err := NewManager(Options{
		HTTPClient: base, Issuer: "https://auth.x.ai", CredentialPath: credentialPath, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := manager.AuthenticatedClient(base)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request, _ := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader("{}"))
			response, err := client.Do(request)
			if err != nil {
				t.Errorf("Do() error = %v", err)
				return
			}
			_ = response.Body.Close()
		}()
	}
	wg.Wait()
	if refreshCalls.Load() != 1 || apiCalls.Load() != 8 {
		t.Fatalf("calls = refresh %d api %d, want 1 and 8", refreshCalls.Load(), apiCalls.Load())
	}
	stored, err := readStoredCredentials(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "refresh-new" || stored.AccessToken != "access-new" {
		t.Fatalf("stored credentials = %#v", stored)
	}
}

func TestRefreshInvalidGrantRequiresReauthentication(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{
		Version: credentialSchemaVersion, RefreshToken: "revoked", AccessToken: "expired", ExpiresAt: now.Add(-time.Minute).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	client := newInMemoryHTTPClient(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		writeJSON(t, writer, tokenEndpointError{Error: "invalid_grant"})
	}))
	manager, err := NewManager(Options{
		HTTPClient: client, Issuer: "https://auth.x.ai", CredentialPath: credentialPath, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.accessToken(context.Background(), nil); !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("accessToken() error = %v, want ErrReauthenticationRequired", err)
	}
}

func TestAuthenticatedClientEnforcesAllowlistAndDoesNotReplayUnauthorized(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{
		Version: credentialSchemaVersion, RefreshToken: "refresh", AccessToken: "access", ExpiresAt: now.Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	base := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if got := request.Header.Get("Authorization"); got != "Bearer access" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("X-XAI-Token-Auth"); got != "xai-grok-cli" {
			t.Errorf("X-XAI-Token-Auth = %q", got)
		}
		return textResponse(request, http.StatusUnauthorized, "expired"), nil
	})}
	manager, err := NewManager(Options{CredentialPath: credentialPath, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	client, err := manager.AuthenticatedClient(base)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader("{}"))
	request.Header.Set("X-Test", "original")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want no replay", calls.Load())
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("X-Test") != "original" {
		t.Fatalf("original request mutated: %#v", request.Header)
	}
	blocked, _ := http.NewRequest(http.MethodGet, "https://example.com/v1/models", nil)
	blockedResponse, err := client.Do(blocked)
	if blockedResponse != nil {
		_ = blockedResponse.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "outside the maintained Grok Build API") {
		t.Fatalf("blocked request error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("blocked request reached base transport: %d", calls.Load())
	}
	apiKeyEndpoint, _ := http.NewRequest(http.MethodGet, "https://api.x.ai/v1/models", nil)
	apiKeyResponse, err := client.Do(apiKeyEndpoint)
	if apiKeyResponse != nil {
		_ = apiKeyResponse.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "outside the maintained Grok Build API") {
		t.Fatalf("api.x.ai request error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("api.x.ai request reached base transport: %d", calls.Load())
	}
}

func TestParseCallbackRejectsStateMismatch(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?code=secret&state=wrong", nil)
	result, terminal := parseCallback(request, "expected")
	if result.err == nil || !strings.Contains(result.err.Error(), "state mismatch") || result.code != "" || terminal {
		t.Fatalf("parseCallback() = %#v, %v", result, terminal)
	}
}

func TestListModelsFiltersNonLanguageEntries(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{
		Version: credentialSchemaVersion, RefreshToken: "refresh", AccessToken: "access", ExpiresAt: now.Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	base := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != xAIModelsURL {
			t.Errorf("URL = %q", request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		return jsonResponse(request, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"id": "grok-4.5"},
			map[string]any{"id": "grok-imagine-image"},
			map[string]any{"id": "grok-4.20"},
			map[string]any{"id": "other"},
			map[string]any{"id": "GROK-4.5"},
		}}), nil
	})}
	manager, err := NewManager(Options{CredentialPath: credentialPath, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	models, err := manager.ListModels(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(models, ","), "grok-4.20,grok-4.5"; got != want {
		t.Fatalf("models = %q, want %q", got, want)
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode JSON: %v", err)
	}
}

func jsonResponse(request *http.Request, status int, value any) *http.Response {
	raw, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(raw))),
		Request:    request,
	}
}

func textResponse(request *http.Request, status int, value string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(value)),
		Request:    request,
	}
}
