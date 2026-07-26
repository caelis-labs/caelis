package grokauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

const (
	deviceGrantType        = "urn:ietf:params:oauth:grant-type:device_code"
	defaultDeviceCodeTTL   = 5 * time.Minute
	defaultDeviceInterval  = 5 * time.Second
	minDeviceInterval      = time.Second
	deviceSlowDownIncrease = 5 * time.Second
)

type callbackResult struct {
	code string
	err  error
}

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

func (m *Manager) loginWithBrowser(ctx context.Context, opts LoginOptions) error {
	state, err := randomURLSafe(m.random, 32)
	if err != nil {
		return fmt.Errorf("grokauth: generate oauth state: %w", err)
	}
	nonce, err := randomURLSafe(m.random, 32)
	if err != nil {
		return fmt.Errorf("grokauth: generate oauth nonce: %w", err)
	}
	verifier, err := randomURLSafe(m.random, 48)
	if err != nil {
		return fmt.Errorf("grokauth: generate PKCE verifier: %w", err)
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])

	listener, err := m.listen("tcp", callbackAddress)
	if err != nil {
		return fmt.Errorf("grokauth: listen for oauth callback on %s: %w", callbackAddress, errors.Join(errBrowserLoginUnavailable, err))
	}
	redirectURI, err := loopbackRedirectURI(listener)
	if err != nil {
		_ = listener.Close()
		return err
	}
	authorizationURL := m.authorizationURL(state, nonce, challenge, redirectURI)
	results := make(chan callbackResult, 1)
	server := callbackServer(state, results)
	serveErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			serveErrors <- err
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider: "xai", Phase: modelconfig.AuthProgressOpeningBrowser, VerificationURL: authorizationURL,
	})
	if err := m.browserOpener(authorizationURL); err != nil {
		return fmt.Errorf("grokauth: open browser: %w", errors.Join(errBrowserLoginUnavailable, err))
	}
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider: "xai", Phase: modelconfig.AuthProgressWaitingForBrowser, VerificationURL: authorizationURL,
	})
	waitCtx := ctx
	if opts.CallbackTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, opts.CallbackTimeout)
		defer cancel()
	}
	inputCtx, cancelInput := context.WithCancel(waitCtx)
	defer cancelInput()
	inputResults := make(chan callbackResult, 1)
	go requestPastedCallback(inputCtx, state, inputResults)
	var result callbackResult
	select {
	case <-waitCtx.Done():
		return fmt.Errorf("grokauth: wait for oauth callback: %w", waitCtx.Err())
	case err := <-serveErrors:
		return fmt.Errorf("grokauth: serve oauth callback: %w", err)
	case result = <-results:
	case result = <-inputResults:
	}
	cancelInput()
	if result.err != nil {
		return result.err
	}
	client := opts.HTTPClient
	if client == nil {
		client = m.httpClient
	}
	tokens, err := m.exchangeCode(ctx, client, result.code, verifier, redirectURI)
	if err != nil {
		return err
	}
	if err := m.installLoginTokens(ctx, tokens); err != nil {
		return err
	}
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider: "xai", Phase: modelconfig.AuthProgressAuthenticated,
	})
	return nil
}

func (m *Manager) authorizationURL(state string, nonce string, challenge string, redirectURI string) string {
	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", oauthScope)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("nonce", nonce)
	query.Set("plan", "generic")
	query.Set("referrer", "caelis")
	return m.issuer + "/oauth2/authorize?" + query.Encode()
}

func callbackServer(expectedState string, results chan<- callbackResult) *http.Server {
	mux := http.NewServeMux()
	var once sync.Once
	mux.HandleFunc("/callback", func(writer http.ResponseWriter, request *http.Request) {
		if !prepareCallbackCORS(writer, request) {
			return
		}
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet+", "+http.MethodOptions)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		result, terminal := parseCallback(request, expectedState)
		status := http.StatusOK
		title := "Grok sign-in complete"
		message := "You can close this window and return to Caelis."
		if result.err != nil {
			status = http.StatusBadRequest
			title = "Grok sign-in failed"
			message = result.err.Error()
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(status)
		_, _ = fmt.Fprintf(writer, "<!doctype html><html><body><h1>%s</h1><p>%s</p></body></html>", html.EscapeString(title), html.EscapeString(message))
		if terminal {
			once.Do(func() { results <- result })
		}
	})
	return &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}

func prepareCallbackCORS(writer http.ResponseWriter, request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin != "" && origin != accountsAppOrigin {
		http.Error(writer, "forbidden callback origin", http.StatusForbidden)
		return false
	}
	writer.Header().Add("Vary", "Origin")
	if origin == accountsAppOrigin {
		writer.Header().Set("Access-Control-Allow-Origin", accountsAppOrigin)
	}
	if request.Method != http.MethodOptions {
		return true
	}
	if origin != accountsAppOrigin || !strings.EqualFold(strings.TrimSpace(request.Header.Get("Access-Control-Request-Method")), http.MethodGet) {
		http.Error(writer, "forbidden callback preflight", http.StatusForbidden)
		return false
	}
	writer.Header().Set("Access-Control-Allow-Methods", http.MethodGet)
	if strings.EqualFold(strings.TrimSpace(request.Header.Get("Access-Control-Request-Private-Network")), "true") {
		writer.Header().Set("Access-Control-Allow-Private-Network", "true")
	}
	return true
}

func parseCallback(request *http.Request, expectedState string) (callbackResult, bool) {
	query := request.URL.Query()
	actualState := query.Get("state")
	if !oauthStateMatches(actualState, expectedState) {
		return callbackResult{err: fmt.Errorf("grokauth: oauth state mismatch")}, false
	}
	if code := sanitizeOAuthError(query.Get("error")); code != "" {
		description := sanitizeOAuthError(query.Get("error_description"))
		if description == "" {
			description = code
		}
		return callbackResult{err: fmt.Errorf("grokauth: oauth authorization failed (%s)", description)}, true
	}
	code := strings.TrimSpace(query.Get("code"))
	if code == "" {
		return callbackResult{err: fmt.Errorf("grokauth: oauth callback omitted authorization code")}, false
	}
	return callbackResult{code: code}, true
}

func requestPastedCallback(ctx context.Context, expectedState string, results chan<- callbackResult) {
	input, err := modelconfig.RequestAuthInput(ctx, modelconfig.AuthInputRequest{
		Provider: "xai",
		Prompt:   "Grok authorization code or callback URL",
		Secret:   true,
	})
	if errors.Is(err, modelconfig.ErrAuthInputUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	result := callbackResult{}
	if err != nil {
		result.err = fmt.Errorf("grokauth: read pasted authorization response: %w", err)
	} else {
		result = parsePastedCallback(input, expectedState)
	}
	select {
	case results <- result:
	case <-ctx.Done():
	}
}

func parsePastedCallback(input string, expectedState string) callbackResult {
	input = strings.TrimSpace(input)
	if input == "" {
		return callbackResult{err: fmt.Errorf("grokauth: pasted authorization response is empty")}
	}
	parsed, err := url.Parse(input)
	if err == nil && parsed.IsAbs() && parsed.Host != "" {
		query := parsed.Query()
		if code := sanitizeOAuthError(query.Get("error")); code != "" {
			description := sanitizeOAuthError(query.Get("error_description"))
			if description == "" {
				description = code
			}
			return callbackResult{err: fmt.Errorf("grokauth: oauth authorization failed (%s)", description)}
		}
		code := strings.TrimSpace(query.Get("code"))
		if code == "" {
			return callbackResult{err: fmt.Errorf("grokauth: pasted callback URL omitted authorization code")}
		}
		if state := strings.TrimSpace(query.Get("state")); state != "" && !oauthStateMatches(state, expectedState) {
			return callbackResult{err: fmt.Errorf("grokauth: oauth state mismatch")}
		}
		return callbackResult{code: code}
	}
	return callbackResult{code: input}
}

func oauthStateMatches(actual string, expected string) bool {
	return len(actual) == len(expected) && subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func loopbackRedirectURI(listener interface{ Addr() net.Addr }) (string, error) {
	if listener == nil || listener.Addr() == nil {
		return "", fmt.Errorf("grokauth: callback listener omitted its address")
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", fmt.Errorf("grokauth: resolve oauth callback address: %w", err)
	}
	if strings.TrimSpace(port) == "" {
		return "", fmt.Errorf("grokauth: oauth callback address omitted a port")
	}
	return "http://127.0.0.1:" + port + "/callback", nil
}

func (m *Manager) loginWithDeviceCode(ctx context.Context, opts LoginOptions) error {
	client := opts.HTTPClient
	if client == nil {
		client = m.httpClient
	}
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider: "xai", Phase: modelconfig.AuthProgressRequestingDeviceCode,
	})
	device, err := m.requestDeviceCode(ctx, client)
	if err != nil {
		return err
	}
	verificationURL := firstNonEmpty(device.VerificationURIComplete, device.VerificationURI)
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider:        "xai",
		Phase:           modelconfig.AuthProgressWaitingForDevice,
		VerificationURL: verificationURL,
		UserCode:        device.UserCode,
	})
	tokens, err := m.pollDeviceCode(ctx, client, device)
	if err != nil {
		return err
	}
	if err := m.installLoginTokens(ctx, tokens); err != nil {
		return err
	}
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider: "xai", Phase: modelconfig.AuthProgressAuthenticated,
	})
	return nil
}

func (m *Manager) requestDeviceCode(ctx context.Context, client *http.Client) (deviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", ClientID)
	form.Set("scope", oauthScope)
	form.Set("referrer", "caelis")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.issuer+"/oauth2/device/code", strings.NewReader(form.Encode()))
	if err != nil {
		return deviceCodeResponse{}, fmt.Errorf("grokauth: build device code request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "caelis")
	response, err := client.Do(request)
	if err != nil {
		return deviceCodeResponse{}, fmt.Errorf("grokauth: request device code: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return deviceCodeResponse{}, readTokenEndpointError("request device code", response)
	}
	var device deviceCodeResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&device); err != nil {
		return deviceCodeResponse{}, fmt.Errorf("grokauth: decode device code response: %w", err)
	}
	device.DeviceCode = strings.TrimSpace(device.DeviceCode)
	device.UserCode = strings.TrimSpace(device.UserCode)
	device.VerificationURI = strings.TrimSpace(device.VerificationURI)
	device.VerificationURIComplete = strings.TrimSpace(device.VerificationURIComplete)
	if device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" {
		return deviceCodeResponse{}, fmt.Errorf("grokauth: device code response omitted required fields")
	}
	return device, nil
}

func (m *Manager) pollDeviceCode(ctx context.Context, client *http.Client, device deviceCodeResponse) (tokenResponse, error) {
	ttl := time.Duration(device.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = defaultDeviceCodeTTL
	}
	interval := time.Duration(device.Interval) * time.Second
	if interval < minDeviceInterval {
		interval = defaultDeviceInterval
	}
	deadline := m.now().Add(ttl)
	for m.now().Before(deadline) {
		form := url.Values{}
		form.Set("grant_type", deviceGrantType)
		form.Set("client_id", ClientID)
		form.Set("device_code", device.DeviceCode)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.issuer+"/oauth2/token", strings.NewReader(form.Encode()))
		if err != nil {
			return tokenResponse{}, fmt.Errorf("grokauth: build device token request: %w", err)
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "caelis")
		response, err := client.Do(request)
		if err != nil {
			return tokenResponse{}, fmt.Errorf("grokauth: poll device token: %w", err)
		}
		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
			var tokens tokenResponse
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokens)
			closeErr := response.Body.Close()
			if decodeErr != nil {
				return tokenResponse{}, fmt.Errorf("grokauth: decode device token response: %w", decodeErr)
			}
			if closeErr != nil {
				return tokenResponse{}, fmt.Errorf("grokauth: close device token response: %w", closeErr)
			}
			if strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" {
				return tokenResponse{}, fmt.Errorf("grokauth: device authorization response omitted required tokens")
			}
			return tokens, nil
		}
		var detail tokenEndpointError
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&detail)
		closeErr := response.Body.Close()
		if closeErr != nil {
			return tokenResponse{}, fmt.Errorf("grokauth: close device token response: %w", closeErr)
		}
		switch sanitizeOAuthError(detail.Error) {
		case "authorization_pending":
		case "slow_down":
			interval += deviceSlowDownIncrease
		case "access_denied", "authorization_denied":
			return tokenResponse{}, fmt.Errorf("grokauth: device authorization was denied")
		case "expired_token":
			return tokenResponse{}, fmt.Errorf("grokauth: device code expired; run /connect grok again")
		default:
			description := firstNonEmpty(sanitizeOAuthError(detail.ErrorDescription), sanitizeOAuthError(detail.Error))
			return tokenResponse{}, fmt.Errorf("grokauth: device token exchange failed with status %d (%s)", response.StatusCode, description)
		}
		remaining := deadline.Sub(m.now())
		if remaining <= 0 {
			break
		}
		if interval > remaining {
			interval = remaining
		}
		if err := m.sleep(ctx, interval); err != nil {
			return tokenResponse{}, err
		}
	}
	return tokenResponse{}, fmt.Errorf("grokauth: device authorization timed out")
}

func randomURLSafe(reader interface{ Read([]byte) (int, error) }, byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
