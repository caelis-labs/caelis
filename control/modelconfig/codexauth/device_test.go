package codexauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestEnsureAuthenticatedUsesDeviceCodeInHeadlessEnvironment(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	issuer, calls := newDeviceAuthTestServer(t, now)
	defer issuer.Close()
	var browserCalls atomic.Int32
	var listenerCalls atomic.Int32
	manager, err := NewManager(Options{
		HTTPClient:     issuer.Client(),
		Issuer:         issuer.URL,
		CredentialPath: DefaultCredentialPath(t.TempDir()),
		Clock:          func() time.Time { return now },
		Headless:       func() bool { return true },
		BrowserOpener: func(string) error {
			browserCalls.Add(1)
			return errors.New("browser opener must not run in headless mode")
		},
		Listen: func(string, string) (net.Listener, error) {
			listenerCalls.Add(1)
			return nil, errors.New("callback listener must not run in headless mode")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var progress []modelconfig.AuthProgress
	ctx := modelconfig.WithAuthProgress(context.Background(), func(update modelconfig.AuthProgress) {
		progress = append(progress, update)
	})
	if err := manager.EnsureAuthenticated(ctx, LoginOptions{OpenBrowser: true}); err != nil {
		t.Fatalf("EnsureAuthenticated() error = %v", err)
	}
	if browserCalls.Load() != 0 || listenerCalls.Load() != 0 {
		t.Fatalf("headless login used browser path: browser=%d listener=%d", browserCalls.Load(), listenerCalls.Load())
	}
	if calls.userCode.Load() != 1 || calls.poll.Load() != 1 || calls.token.Load() != 1 {
		t.Fatalf("device calls = usercode:%d poll:%d token:%d, want one each", calls.userCode.Load(), calls.poll.Load(), calls.token.Load())
	}
	assertDeviceAuthProgress(t, progress, issuer.URL)
}

func TestEnsureAuthenticatedFallsBackToDeviceCodeWhenBrowserCannotOpen(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	issuer, calls := newDeviceAuthTestServer(t, now)
	defer issuer.Close()
	listener, _ := newMemoryCallbackHarness(t)
	var browserCalls atomic.Int32
	manager, err := NewManager(Options{
		HTTPClient:     issuer.Client(),
		Issuer:         issuer.URL,
		CredentialPath: DefaultCredentialPath(t.TempDir()),
		Clock:          func() time.Time { return now },
		Headless:       func() bool { return false },
		Listen: func(string, string) (net.Listener, error) {
			return listener, nil
		},
		BrowserOpener: func(string) error {
			browserCalls.Add(1)
			return errors.New("no graphical browser")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureAuthenticated(context.Background(), LoginOptions{OpenBrowser: true}); err != nil {
		t.Fatalf("EnsureAuthenticated() error = %v", err)
	}
	if browserCalls.Load() != 1 {
		t.Fatalf("browser calls = %d, want one attempt before fallback", browserCalls.Load())
	}
	if calls.userCode.Load() != 1 || calls.poll.Load() != 1 || calls.token.Load() != 1 {
		t.Fatalf("device fallback calls = usercode:%d poll:%d token:%d, want one each", calls.userCode.Load(), calls.poll.Load(), calls.token.Load())
	}
}

func TestEnsureAuthenticatedHeadlessDeviceUnavailableWaitsForManualBrowserCallback(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	var userCodeCalls atomic.Int32
	var tokenCalls atomic.Int32
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			userCodeCalls.Add(1)
			http.NotFound(writer, request)
		case "/oauth/token":
			tokenCalls.Add(1)
			if err := request.ParseForm(); err != nil {
				t.Errorf("ParseForm() error = %v", err)
			}
			if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("code") != "manual-code" || request.Form.Get("redirect_uri") != RedirectURI {
				t.Errorf("manual browser exchange = %#v", request.Form)
			}
			writeJSON(t, writer, tokenResponse{
				AccessToken:  jwtForTest("account-manual", nil, now.Add(time.Hour)),
				RefreshToken: "refresh-manual",
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer issuer.Close()
	listener, callbackClient := newMemoryCallbackHarness(t)
	var browserCalls atomic.Int32
	manager, err := NewManager(Options{
		HTTPClient:     issuer.Client(),
		Issuer:         issuer.URL,
		CredentialPath: DefaultCredentialPath(t.TempDir()),
		Clock:          func() time.Time { return now },
		Headless:       func() bool { return true },
		Listen: func(string, string) (net.Listener, error) {
			return listener, nil
		},
		BrowserOpener: func(string) error {
			browserCalls.Add(1)
			return errors.New("browser opener must not run for manual fallback")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	progress := make(chan modelconfig.AuthProgress, 8)
	ctx := modelconfig.WithAuthProgress(context.Background(), func(update modelconfig.AuthProgress) {
		progress <- update
	})
	done := make(chan error, 1)
	go func() {
		done <- manager.EnsureAuthenticated(ctx, LoginOptions{OpenBrowser: true, CallbackTimeout: 2 * time.Second})
	}()

	var authorizationURL string
	for authorizationURL == "" {
		select {
		case update := <-progress:
			if update.Phase == modelconfig.AuthProgressWaitingForBrowser {
				authorizationURL = update.VerificationURL
			}
		case err := <-done:
			t.Fatalf("EnsureAuthenticated() returned before manual callback: %v", err)
		case <-time.After(time.Second):
			t.Fatal("manual browser fallback did not publish an authorization URL")
		}
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("manual authorization URL omitted state: %q", authorizationURL)
	}
	callback := "http://" + listener.Addr().String() + "/auth/callback?code=manual-code&state=" + url.QueryEscape(state)
	response, err := callbackClient.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureAuthenticated() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual browser callback did not finish authentication")
	}
	if browserCalls.Load() != 0 || userCodeCalls.Load() != 1 || tokenCalls.Load() != 1 {
		t.Fatalf("fallback calls = browser:%d usercode:%d token:%d", browserCalls.Load(), userCodeCalls.Load(), tokenCalls.Load())
	}
}

type deviceAuthCalls struct {
	userCode atomic.Int32
	poll     atomic.Int32
	token    atomic.Int32
}

func newDeviceAuthTestServer(t *testing.T, now time.Time) (*inMemoryHTTPServer, *deviceAuthCalls) {
	t.Helper()
	const verifier = "device-pkce-verifier"
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	calls := &deviceAuthCalls{}
	issuer := newInMemoryHTTPServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			calls.userCode.Add(1)
			var payload map[string]string
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode usercode request: %v", err)
			}
			if got := payload["client_id"]; got != ClientID {
				t.Errorf("usercode client_id = %q", got)
			}
			writeJSON(t, writer, map[string]string{
				"device_auth_id": "device-auth-1",
				"user_code":      "ABCD-EFGH",
				"interval":       "1",
			})
		case "/api/accounts/deviceauth/token":
			calls.poll.Add(1)
			var payload map[string]string
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode device token request: %v", err)
			}
			if payload["device_auth_id"] != "device-auth-1" || payload["user_code"] != "ABCD-EFGH" {
				t.Errorf("device token request = %#v", payload)
			}
			writeJSON(t, writer, deviceAuthorizationResponse{
				AuthorizationCode: "device-authorization-code",
				CodeChallenge:     challenge,
				CodeVerifier:      verifier,
			})
		case "/oauth/token":
			calls.token.Add(1)
			if got := request.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
				t.Errorf("token Content-Type = %q", got)
			}
			if err := request.ParseForm(); err != nil {
				t.Errorf("ParseForm() error = %v", err)
			}
			if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("client_id") != ClientID || request.Form.Get("code") != "device-authorization-code" || request.Form.Get("code_verifier") != verifier {
				t.Errorf("device authorization exchange = %#v", request.Form)
			}
			if got, want := request.Form.Get("redirect_uri"), testIssuerURL+"/deviceauth/callback"; got != want {
				t.Errorf("device redirect_uri = %q, want %q", got, want)
			}
			writeJSON(t, writer, tokenResponse{
				AccessToken:  jwtForTest("account-device", nil, now.Add(time.Hour)),
				RefreshToken: "refresh-device",
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	return issuer, calls
}

func assertDeviceAuthProgress(t *testing.T, progress []modelconfig.AuthProgress, issuer string) {
	t.Helper()
	wantPhases := []modelconfig.AuthProgressPhase{
		modelconfig.AuthProgressRequestingDeviceCode,
		modelconfig.AuthProgressWaitingForDevice,
		modelconfig.AuthProgressAuthenticated,
	}
	if len(progress) != len(wantPhases) {
		t.Fatalf("auth progress = %#v, want phases %#v", progress, wantPhases)
	}
	for index, want := range wantPhases {
		if progress[index].Phase != want {
			t.Fatalf("auth progress[%d].Phase = %q, want %q", index, progress[index].Phase, want)
		}
	}
	if progress[1].VerificationURL != issuer+"/codex/device" || progress[1].UserCode != "ABCD-EFGH" {
		t.Fatalf("device guidance = %#v", progress[1])
	}
}
