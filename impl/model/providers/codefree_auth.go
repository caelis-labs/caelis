package providers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CodeFreeClientAuthMethod string

const (
	CodeFreeClientAuthNone  CodeFreeClientAuthMethod = "none"
	CodeFreeClientAuthBody  CodeFreeClientAuthMethod = "body"
	CodeFreeClientAuthBasic CodeFreeClientAuthMethod = "basic"
)

type CodeFreeLoginOptions struct {
	BaseURL          string
	HTTPClient       *http.Client
	CredentialPath   string
	ClientID         string
	ClientSecret     string
	ClientAuthMethod CodeFreeClientAuthMethod
	RedirectHost     string
	RedirectPort     int
	CallbackTimeout  time.Duration
	OpenBrowser      bool
	NotifyAuthURL    func(string)
}

type CodeFreeEnsureAuthOptions struct {
	BaseURL          string
	HTTPClient       *http.Client
	CredentialPath   string
	ClientID         string
	ClientSecret     string
	ClientAuthMethod CodeFreeClientAuthMethod
	RedirectHost     string
	RedirectPort     int
	CallbackTimeout  time.Duration
	OpenBrowser      bool
	NotifyAuthURL    func(string)
}

type CodeFreeAuthResult struct {
	CredentialPath string
	BaseURL        string
	UserID         string
}

type codeFreeOAuthTokenResponse struct {
	AccessToken           string `json:"access_token"`
	TokenType             string `json:"token_type"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	UserID                string `json:"id_token"`
	SessionID             string `json:"ori_session_id"`
	OriginalToken         string `json:"ori_token"`
	RawDebug              string `json:"-"`
}

type codeFreeAPIKeyResponse struct {
	EncryptedAPIKey string `json:"encryptedApiKey"`
	OptResult       int    `json:"optResult"`
}

type codeFreeTokenEndpointError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *codeFreeTokenEndpointError) Error() string {
	if e == nil {
		return "providers: codefree token endpoint error"
	}
	switch {
	case strings.TrimSpace(e.Code) != "" && strings.TrimSpace(e.Description) != "":
		return fmt.Sprintf("providers: codefree token endpoint error %s: %s", strings.TrimSpace(e.Code), strings.TrimSpace(e.Description))
	case strings.TrimSpace(e.Code) != "":
		return fmt.Sprintf("providers: codefree token endpoint error %s", strings.TrimSpace(e.Code))
	case strings.TrimSpace(e.Description) != "":
		return fmt.Sprintf("providers: codefree token endpoint error: %s", strings.TrimSpace(e.Description))
	default:
		return "providers: codefree token endpoint error"
	}
}

type codeFreeOAuthConfig struct {
	BaseURL                string
	HTTPClient             *http.Client
	CredentialPath         string
	CredentialPathExplicit bool
	ClientID               string
	ClientSecret           string
	ClientAuthMethod       CodeFreeClientAuthMethod
}

type codeFreeOAuthCallback struct {
	Code  string
	State string
	Err   string
}

var codeFreeOpenBrowser = defaultCodeFreeOpenBrowser

type codeFreeLoginFlowSession interface {
	State() string
	CodeChallenge() string
	CodeVerifier() string
	Wait(context.Context) (codeFreeOAuthCallback, error)
	Close() error
}

var newCodeFreeLoginFlowSession = func(host string, port int) (codeFreeLoginFlowSession, error) {
	return newCodeFreeLoginFlow(host, port)
}

var (
	codeFreeEnsureAuthMu       sync.Mutex
	codeFreeEnsureAuthInflight = map[string]*codeFreeEnsureAuthCall{}
)

type codeFreeEnsureAuthCall struct {
	done   chan struct{}
	result CodeFreeAuthResult
	err    error
}

func CodeFreeEnsureAuth(ctx context.Context, opts CodeFreeEnsureAuthOptions) (CodeFreeAuthResult, error) {
	cfg, err := resolveCodeFreeOAuthConfig(opts.BaseURL, opts.HTTPClient, opts.CredentialPath, opts.ClientID, opts.ClientSecret, opts.ClientAuthMethod)
	if err != nil {
		return CodeFreeAuthResult{}, err
	}
	if result, ok := loadExistingCodeFreeAuthResult(cfg); ok {
		return result, nil
	}
	return codeFreeEnsureAuthWithLogin(ctx, cfg, opts)
}

func loadExistingCodeFreeAuthResult(cfg codeFreeOAuthConfig) (CodeFreeAuthResult, bool) {
	codeFreeCredentialMu.Lock()
	defer codeFreeCredentialMu.Unlock()
	credentialPath := cfg.CredentialPath
	if !cfg.CredentialPathExplicit {
		credentialPath = ""
	}
	stored, err := loadCodeFreeStoredCredentialsLocked(cfg.BaseURL, credentialPath)
	if err != nil {
		return CodeFreeAuthResult{}, false
	}
	if _, err := finalizeCodeFreeCredentials(stored); err != nil {
		return CodeFreeAuthResult{}, false
	}
	return toCodeFreeAuthResult(stored), true
}

func codeFreeEnsureAuthWithLogin(ctx context.Context, cfg codeFreeOAuthConfig, opts CodeFreeEnsureAuthOptions) (CodeFreeAuthResult, error) {
	key := cfg.CredentialPath + "|" + cfg.BaseURL
	codeFreeEnsureAuthMu.Lock()
	if call := codeFreeEnsureAuthInflight[key]; call != nil {
		codeFreeEnsureAuthMu.Unlock()
		select {
		case <-ctx.Done():
			return CodeFreeAuthResult{}, ctx.Err()
		case <-call.done:
			return call.result, call.err
		}
	}
	call := &codeFreeEnsureAuthCall{done: make(chan struct{})}
	codeFreeEnsureAuthInflight[key] = call
	codeFreeEnsureAuthMu.Unlock()

	defer func() {
		codeFreeEnsureAuthMu.Lock()
		delete(codeFreeEnsureAuthInflight, key)
		codeFreeEnsureAuthMu.Unlock()
		close(call.done)
	}()

	result, err := CodeFreeLogin(ctx, CodeFreeLoginOptions{
		BaseURL:          cfg.BaseURL,
		HTTPClient:       cfg.HTTPClient,
		CredentialPath:   cfg.CredentialPath,
		ClientID:         cfg.ClientID,
		ClientSecret:     cfg.ClientSecret,
		ClientAuthMethod: cfg.ClientAuthMethod,
		RedirectHost:     opts.RedirectHost,
		RedirectPort:     opts.RedirectPort,
		CallbackTimeout:  opts.CallbackTimeout,
		OpenBrowser:      opts.OpenBrowser,
		NotifyAuthURL:    opts.NotifyAuthURL,
	})
	call.result = result
	call.err = err
	if err != nil {
		return CodeFreeAuthResult{}, err
	}
	return result, nil
}

func CodeFreeEnsureModelSelectionAuth(ctx context.Context, opts CodeFreeEnsureAuthOptions) (bool, error) {
	cfg, err := resolveCodeFreeOAuthConfig(opts.BaseURL, opts.HTTPClient, opts.CredentialPath, opts.ClientID, opts.ClientSecret, opts.ClientAuthMethod)
	if err != nil {
		return false, err
	}
	if _, ok := loadExistingCodeFreeAuthResult(cfg); ok {
		return false, nil
	}
	if _, err := codeFreeEnsureAuthWithLogin(ctx, cfg, opts); err != nil {
		return false, err
	}
	return true, nil
}

func CodeFreeLogin(ctx context.Context, opts CodeFreeLoginOptions) (CodeFreeAuthResult, error) {
	cfg, err := resolveCodeFreeOAuthConfig(opts.BaseURL, opts.HTTPClient, opts.CredentialPath, opts.ClientID, opts.ClientSecret, opts.ClientAuthMethod)
	if err != nil {
		return CodeFreeAuthResult{}, err
	}
	timeout := opts.CallbackTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	host := strings.TrimSpace(opts.RedirectHost)
	if host == "" {
		host = "127.0.0.1"
	}
	flow, err := newCodeFreeLoginFlowSession(host, opts.RedirectPort)
	if err != nil {
		return CodeFreeAuthResult{}, err
	}
	defer flow.Close()

	authURL := buildCodeFreeAuthorizationURL(cfg, flow.State(), flow.CodeChallenge())
	if opts.NotifyAuthURL != nil {
		opts.NotifyAuthURL(authURL)
	}
	if opts.OpenBrowser || opts.NotifyAuthURL == nil {
		if err := codeFreeOpenBrowser(authURL); err != nil && opts.NotifyAuthURL == nil {
			return CodeFreeAuthResult{}, err
		}
	}

	callbackCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	callback, err := flow.Wait(callbackCtx)
	if err != nil {
		return CodeFreeAuthResult{}, err
	}
	if callback.Err != "" {
		return CodeFreeAuthResult{}, fmt.Errorf("providers: codefree oauth callback error: %s", callback.Err)
	}
	// SRDCloud's registered redirect forwards the local callback URL from state,
	// but the final localhost request may omit the state query parameter entirely.
	// The random local path still binds the callback to this login flow.
	if callback.State != "" && callback.State != flow.State() {
		return CodeFreeAuthResult{}, fmt.Errorf("providers: codefree oauth state mismatch")
	}
	tokens, err := exchangeCodeFreeAuthorizationCode(ctx, cfg, flow.CodeVerifier(), callback.Code)
	if err != nil {
		return CodeFreeAuthResult{}, err
	}
	codeFreeCredentialMu.Lock()
	defer codeFreeCredentialMu.Unlock()
	stored, err := persistCodeFreeTokenSet(ctx, cfg, tokens)
	if err != nil {
		return CodeFreeAuthResult{}, err
	}
	return toCodeFreeAuthResult(stored), nil
}

func persistCodeFreeTokenSet(ctx context.Context, cfg codeFreeOAuthConfig, tokens codeFreeOAuthTokenResponse) (codeFreeStoredCredentials, error) {
	userID := strings.TrimSpace(tokens.UserID)
	if userID == "" {
		if strings.TrimSpace(tokens.RawDebug) != "" {
			return codeFreeStoredCredentials{}, fmt.Errorf("providers: codefree oauth response missing uid/userId; token response=%s", tokens.RawDebug)
		}
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: codefree oauth response missing uid/userId")
	}
	sessionID := strings.TrimSpace(tokens.SessionID)
	if sessionID == "" {
		if strings.TrimSpace(tokens.RawDebug) != "" {
			return codeFreeStoredCredentials{}, fmt.Errorf("providers: codefree oauth response missing ori_session_id; token response=%s", tokens.RawDebug)
		}
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: codefree oauth response missing ori_session_id")
	}
	encryptedAPIKey, err := fetchCodeFreeEncryptedAPIKey(ctx, cfg, sessionID, userID)
	if err != nil {
		if strings.TrimSpace(tokens.RawDebug) != "" {
			return codeFreeStoredCredentials{}, fmt.Errorf("%w token response=%s", err, tokens.RawDebug)
		}
		return codeFreeStoredCredentials{}, err
	}
	cached := codeFreeCachedCredentials{
		EncryptedAPIKey: encryptedAPIKey,
		UserID:          userID,
		SessionID:       sessionID,
		BaseURLSnapshot: cfg.BaseURL,
	}
	if err := saveCodeFreeStoredCredentials(cfg.CredentialPath, cached); err != nil {
		return codeFreeStoredCredentials{}, err
	}
	return readCodeFreeStoredCredentialsAtPath(cfg.CredentialPath)
}

func fetchCodeFreeEncryptedAPIKey(ctx context.Context, cfg codeFreeOAuthConfig, accessToken string, userID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codeFreeAPIKeyEndpoint(cfg.BaseURL), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("sessionId", strings.TrimSpace(accessToken))
	req.Header.Set("userId", strings.TrimSpace(userID))
	req.Header.Set("projectId", "0")
	resp, err := coalesceCodeFreeControlHTTPClient(cfg.HTTPClient).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		body := strings.TrimSpace(string(raw))
		if body == "" {
			return "", fmt.Errorf("providers: codefree user apikey request failed userId=%s accessToken=%s http status %d", redactCodeFreeSensitiveField("id_token", userID), redactCodeFreeSensitiveField("access_token", accessToken), resp.StatusCode)
		}
		return "", fmt.Errorf("providers: codefree user apikey request failed userId=%s accessToken=%s http status %d body=%s", redactCodeFreeSensitiveField("id_token", userID), redactCodeFreeSensitiveField("access_token", accessToken), resp.StatusCode, body)
	}
	var payload codeFreeAPIKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	value := strings.TrimSpace(payload.EncryptedAPIKey)
	if value == "" {
		return "", fmt.Errorf("providers: codefree user apikey response missing encryptedApiKey")
	}
	return value, nil
}

func exchangeCodeFreeAuthorizationCode(ctx context.Context, cfg codeFreeOAuthConfig, codeVerifier string, code string) (codeFreeOAuthTokenResponse, error) {
	values := url.Values{}
	values.Set("client_id", cfg.ClientID)
	if verifier := strings.TrimSpace(codeVerifier); verifier != "" {
		values.Set("code_verifier", verifier)
	}
	values.Set("code", strings.TrimSpace(code))
	values.Set("grant_type", "authorization_code")
	values.Set("redirect_uri", codeFreeRegisteredRedirectURI(cfg.BaseURL))
	return doCodeFreeAuthCodeTokenRequest(ctx, cfg, values)
}

func doCodeFreeAuthCodeTokenRequest(ctx context.Context, cfg codeFreeOAuthConfig, values url.Values) (codeFreeOAuthTokenResponse, error) {
	endpoint := codeFreeTokenEndpoint(cfg.BaseURL)
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return codeFreeOAuthTokenResponse{}, err
	}
	resp, err := coalesceCodeFreeControlHTTPClient(cfg.HTTPClient).Do(req)
	if err != nil {
		return codeFreeOAuthTokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return codeFreeOAuthTokenResponse{}, readCodeFreeTokenEndpointError(resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return codeFreeOAuthTokenResponse{}, err
	}
	payload, err := decodeCodeFreeTokenResponse(raw)
	if err != nil {
		return codeFreeOAuthTokenResponse{}, fmt.Errorf("%w; token response=%s", err, redactCodeFreeTokenDebug(raw))
	}
	payload.RawDebug = redactCodeFreeTokenDebug(raw)
	return payload, nil
}

func readCodeFreeTokenEndpointError(resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("providers: empty codefree token response")
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(raw))
	if values, err := url.ParseQuery(body); err == nil && (values.Get("error") != "" || values.Get("error_description") != "") {
		return &codeFreeTokenEndpointError{
			Code:        values.Get("error"),
			Description: values.Get("error_description"),
		}
	}
	var payload codeFreeTokenEndpointError
	if json.Unmarshal(raw, &payload) == nil && (payload.Code != "" || payload.Description != "") {
		return &payload
	}
	if body == "" {
		return fmt.Errorf("providers: codefree token endpoint http status %d", resp.StatusCode)
	}
	return fmt.Errorf("providers: codefree token endpoint http status %d body=%s", resp.StatusCode, body)
}

func decodeCodeFreeTokenResponse(raw []byte) (codeFreeOAuthTokenResponse, error) {
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return codeFreeOAuthTokenResponse{}, fmt.Errorf("providers: empty codefree token response")
	}
	var payload codeFreeOAuthTokenResponse
	if err := json.Unmarshal(raw, &payload); err == nil {
		payload.UserID = codeFreeFirstNonEmpty(payload.UserID, extractCodeFreeJSONTokenField(raw, "userId"), extractCodeFreeJSONTokenField(raw, "user_id"), extractCodeFreeJSONTokenField(raw, "uid"))
		payload.SessionID = codeFreeFirstNonEmpty(payload.SessionID, extractCodeFreeJSONTokenField(raw, "ori_session_id"), extractCodeFreeJSONTokenField(raw, "session_id"))
		payload.OriginalToken = codeFreeFirstNonEmpty(payload.OriginalToken, extractCodeFreeJSONTokenField(raw, "ori_token"))
		return payload, nil
	}
	values, err := url.ParseQuery(body)
	if err != nil {
		return codeFreeOAuthTokenResponse{}, err
	}
	if values.Get("ori_session_id") == "" &&
		values.Get("session_id") == "" &&
		values.Get("uid") == "" &&
		values.Get("userId") == "" &&
		values.Get("user_id") == "" &&
		values.Get("id_token") == "" {
		return codeFreeOAuthTokenResponse{}, fmt.Errorf("providers: unsupported codefree token response format")
	}
	payload.AccessToken = strings.TrimSpace(values.Get("access_token"))
	payload.TokenType = strings.TrimSpace(values.Get("token_type"))
	payload.RefreshToken = strings.TrimSpace(values.Get("refresh_token"))
	payload.UserID = codeFreeFirstNonEmpty(values.Get("id_token"), values.Get("userId"), values.Get("user_id"), values.Get("uid"))
	payload.SessionID = codeFreeFirstNonEmpty(values.Get("ori_session_id"), values.Get("session_id"))
	payload.OriginalToken = strings.TrimSpace(values.Get("ori_token"))
	payload.ExpiresIn = parseCodeFreeInt64(values.Get("expires_in"))
	payload.RefreshTokenExpiresIn = parseCodeFreeInt64(values.Get("refresh_token_expires_in"))
	return payload, nil
}

func parseCodeFreeInt64(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func redactCodeFreeTokenDebug(raw []byte) string {
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return `""`
	}
	if values, err := url.ParseQuery(body); err == nil && len(values) > 0 {
		sanitized := url.Values{}
		for key, vals := range values {
			for _, one := range vals {
				sanitized.Add(key, redactCodeFreeSensitiveField(key, one))
			}
		}
		return sanitized.Encode()
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err == nil && len(payload) > 0 {
		for key, value := range payload {
			payload[key] = redactCodeFreeSensitiveValue(key, value)
		}
		encoded, err := json.Marshal(payload)
		if err == nil {
			return string(encoded)
		}
	}
	return redactCodeFreeSensitiveField("raw", body)
}

func redactCodeFreeSensitiveValue(key string, value any) any {
	if !isCodeFreeSensitiveField(key) {
		return value
	}
	if text, ok := value.(string); ok {
		return redactCodeFreeSensitiveField(key, text)
	}
	if value == nil {
		return nil
	}
	return "[redacted]"
}

func redactCodeFreeSensitiveField(key string, value string) string {
	value = strings.TrimSpace(value)
	if isCodeFreeSensitiveField(key) {
		if value == "" {
			return ""
		}
		return fmt.Sprintf("[redacted len=%d]", len(value))
	}
	return value
}

func isCodeFreeSensitiveField(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "access_token", "refresh_token", "id_token", "apikey", "api_key", "authorization", "code", "ori_token", "ori_session_id", "session_id", "sessionid", "user_id", "userid":
		return true
	default:
		return false
	}
}

func extractCodeFreeJSONTokenField(raw []byte, key string) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func resolveCodeFreeOAuthConfig(baseURL string, httpClient *http.Client, credentialPath string, clientID string, clientSecret string, authMethod CodeFreeClientAuthMethod) (codeFreeOAuthConfig, error) {
	path := strings.TrimSpace(credentialPath)
	pathExplicit := false
	if path == "" {
		if envPath := strings.TrimSpace(os.Getenv(codeFreeCredsPathEnv)); envPath != "" {
			path = envPath
			pathExplicit = true
		} else {
			var err error
			path, err = resolveCodeFreeDefaultCredentialPath()
			if err != nil {
				return codeFreeOAuthConfig{}, err
			}
		}
	} else {
		pathExplicit = true
	}
	resolvedClientSecret := strings.TrimSpace(codeFreeFirstNonEmpty(clientSecret, os.Getenv(codeFreeClientSecretEnv)))
	cfg := codeFreeOAuthConfig{
		BaseURL:                normalizeCodeFreeBaseURL(baseURL),
		HTTPClient:             httpClient,
		CredentialPath:         path,
		CredentialPathExplicit: pathExplicit,
		ClientID:               codeFreeFirstNonEmpty(clientID, os.Getenv(codeFreeClientIDEnv), codeFreeOAuthClientIDForBaseURL(baseURL)),
		ClientSecret:           resolvedClientSecret,
		ClientAuthMethod:       normalizeCodeFreeClientAuthMethod(authMethod, resolvedClientSecret),
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = codeFreeDefaultBaseURL
	}
	if cfg.ClientID == "" {
		return codeFreeOAuthConfig{}, fmt.Errorf("providers: codefree oauth client id is empty")
	}
	return cfg, nil
}

func codeFreeOAuthClientIDForBaseURL(baseURL string) string {
	base := strings.ToLower(normalizeCodeFreeBaseURL(baseURL))
	switch base {
	case "https://dev.srdcloud.cn":
		return codeFreeDevOAuthClientID
	case "https://test.srdcloud.cn":
		return codeFreeTestOAuthClientID
	default:
		return codeFreeDefaultOAuthClientID
	}
}

func normalizeCodeFreeBaseURL(baseURL string) string {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = codeFreeDefaultBaseURL
	}
	return strings.TrimRight(base, "/")
}

func normalizeCodeFreeClientAuthMethod(method CodeFreeClientAuthMethod, clientSecret string) CodeFreeClientAuthMethod {
	if method == "" {
		method = CodeFreeClientAuthMethod(strings.TrimSpace(os.Getenv(codeFreeClientAuthMethodEnv)))
	}
	switch strings.ToLower(strings.TrimSpace(string(method))) {
	case "":
		if strings.TrimSpace(clientSecret) == "" {
			return CodeFreeClientAuthNone
		}
		return CodeFreeClientAuthBody
	case "body":
		return CodeFreeClientAuthBody
	case "basic":
		return CodeFreeClientAuthBasic
	case "none":
		return CodeFreeClientAuthNone
	default:
		return CodeFreeClientAuthBody
	}
}

func buildCodeFreeAuthorizationURL(cfg codeFreeOAuthConfig, state string, codeChallenge string) string {
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", cfg.ClientID)
	values.Set("redirect_uri", codeFreeRegisteredRedirectURI(cfg.BaseURL))
	values.Set("code_challenge", codeChallenge)
	values.Set("code_challenge_method", "S256")
	values.Set("state", state)
	return codeFreeAuthorizeEndpoint(cfg.BaseURL) + "?" + values.Encode()
}

type codeFreeLoginFlow struct {
	listener      net.Listener
	server        *http.Server
	events        chan codeFreeOAuthCallback
	state         string
	codeVerifier  string
	codeChallenge string
	localURL      string
	localPath     string
}

func newCodeFreeLoginFlow(host string, port int) (*codeFreeLoginFlow, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("providers: listen for codefree oauth callback: %w", err)
	}
	callbackID, err := codeFreeRandomDigits(4)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	codeVerifier, err := codeFreeRandomURLSafe(48)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	codeChallenge := codeFreeS256(codeVerifier)
	localPath := "/oauth2callback"
	localURL := "http://" + listener.Addr().String() + localPath + "?from=codefree-o&randomCode=" + callbackID
	state := base64.StdEncoding.EncodeToString([]byte(localURL))
	flow := &codeFreeLoginFlow{
		listener:      listener,
		events:        make(chan codeFreeOAuthCallback, 1),
		state:         state,
		codeVerifier:  codeVerifier,
		codeChallenge: codeChallenge,
		localURL:      localURL,
		localPath:     localPath,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.URL.Path) != flow.localPath {
			http.NotFound(w, r)
			return
		}
		if strings.TrimSpace(r.URL.Query().Get("from")) != "codefree-o" || strings.TrimSpace(r.URL.Query().Get("randomCode")) != callbackID {
			http.NotFound(w, r)
			return
		}
		callback := codeFreeOAuthCallback{
			Code:  strings.TrimSpace(r.URL.Query().Get("code")),
			State: strings.TrimSpace(r.URL.Query().Get("state")),
			Err:   strings.TrimSpace(r.URL.Query().Get("error")),
		}
		select {
		case flow.events <- callback:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if callback.Err != "" {
			_, _ = io.WriteString(w, "<html><body>CodeFree login failed. You can close this tab.</body></html>")
			return
		}
		_, _ = io.WriteString(w, "<html><body>CodeFree login completed. You can close this tab.</body></html>")
	})
	flow.server = &http.Server{Handler: mux}
	go func() {
		_ = flow.server.Serve(listener)
	}()
	return flow, nil
}

func (f *codeFreeLoginFlow) Wait(ctx context.Context) (codeFreeOAuthCallback, error) {
	if f == nil {
		return codeFreeOAuthCallback{}, fmt.Errorf("providers: codefree oauth flow is nil")
	}
	select {
	case callback := <-f.events:
		if callback.Code == "" && callback.Err == "" {
			return codeFreeOAuthCallback{}, fmt.Errorf("providers: codefree oauth callback missing code")
		}
		return callback, nil
	case <-ctx.Done():
		return codeFreeOAuthCallback{}, fmt.Errorf("providers: wait for codefree oauth callback: %w", ctx.Err())
	}
}

func (f *codeFreeLoginFlow) Close() error {
	if f == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = f.server.Shutdown(shutdownCtx)
	return f.listener.Close()
}

func (f *codeFreeLoginFlow) State() string {
	if f == nil {
		return ""
	}
	return f.state
}

func (f *codeFreeLoginFlow) CodeChallenge() string {
	if f == nil {
		return ""
	}
	return f.codeChallenge
}

func (f *codeFreeLoginFlow) CodeVerifier() string {
	if f == nil {
		return ""
	}
	return f.codeVerifier
}

func codeFreeRandomURLSafe(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("providers: generate codefree oauth random value: %w", err)
	}
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(buf), "="), nil
}

func codeFreeRandomDigits(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("providers: invalid codefree callback digit length")
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("providers: generate codefree oauth callback id: %w", err)
	}
	out := make([]byte, length)
	for i := range buf {
		out[i] = '0' + (buf[i] % 10)
	}
	return string(out), nil
}

func codeFreeS256(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(sum[:]), "=")
}

func defaultCodeFreeOpenBrowser(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("providers: codefree oauth url is empty")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("providers: open browser for codefree oauth: %w", err)
	}
	return nil
}

func codeFreeAuthorizeEndpoint(baseURL string) string {
	return normalizeCodeFreeBaseURL(baseURL) + codeFreeOAuthAuthorizePath
}

func codeFreeTokenEndpoint(baseURL string) string {
	return normalizeCodeFreeBaseURL(baseURL) + codeFreeOAuthTokenPath
}

func codeFreeAPIKeyEndpoint(baseURL string) string {
	return normalizeCodeFreeBaseURL(baseURL) + codeFreeUserAPIKeyPath
}

func codeFreeRegisteredRedirectURI(baseURL string) string {
	return normalizeCodeFreeBaseURL(baseURL) + codeFreeOAuthRedirectPath
}

func saveCodeFreeStoredCredentials(path string, cached codeFreeCachedCredentials) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("providers: ensure codefree credential dir %q: %w", dir, err)
	}
	cached = normalizeCodeFreeCachedCredentials(cached)
	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return fmt.Errorf("providers: encode codefree credentials: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("providers: write codefree credentials %q: %w", path, err)
	}
	return nil
}

func readCodeFreeStoredCredentialsAtPath(path string) (codeFreeStoredCredentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: read codefree credentials %q: %w", path, err)
	}
	var cached codeFreeCachedCredentials
	if err := json.Unmarshal(raw, &cached); err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: decode codefree credentials %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return codeFreeStoredCredentials{}, fmt.Errorf("providers: stat codefree credentials %q: %w", path, err)
	}
	return codeFreeStoredCredentials{Cached: cached, Path: path, ModTime: info.ModTime()}, nil
}

func toCodeFreeAuthResult(stored codeFreeStoredCredentials) CodeFreeAuthResult {
	return CodeFreeAuthResult{
		CredentialPath: stored.Path,
		BaseURL:        codeFreeCredentialBaseURL(stored.Cached),
		UserID:         strings.TrimSpace(stored.Cached.UserID),
	}
}
