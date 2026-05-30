package codefree

import (
	"context"
	"crypto/rand"
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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	oauthAuthorizePath   = "/login/oauth/authorize"
	oauthTokenPath       = "/login/oauth/access_token"
	oauthRedirectPath    = "/login/oauth-srdcloud-redirect"
	userAPIKeyPath       = "/api/acbackend/user/v1/apikey"
	defaultOAuthClientID = "251384680635inwsxjcm"
	clientIDEnv          = "CODEFREE_OAUTH_CLIENT_ID"
	clientSecretEnv      = "CODEFREE_OAUTH_CLIENT_SECRET"
	clientAuthMethodEnv  = "CODEFREE_OAUTH_CLIENT_AUTH_METHOD"
	defaultCallbackHost  = "127.0.0.1"
	defaultCallbackWait  = 5 * time.Minute
)

type ClientAuthMethod string

const (
	ClientAuthNone  ClientAuthMethod = "none"
	ClientAuthBody  ClientAuthMethod = "body"
	ClientAuthBasic ClientAuthMethod = "basic"
)

type AuthOptions struct {
	BaseURL          string
	HTTPClient       *http.Client
	CredentialPath   string
	ClientID         string
	ClientSecret     string
	ClientAuthMethod ClientAuthMethod
	RedirectHost     string
	RedirectPort     int
	CallbackTimeout  time.Duration
	OpenBrowser      bool
	NotifyAuthURL    func(string)
}

type RefreshOptions struct {
	BaseURL          string
	HTTPClient       *http.Client
	CredentialPath   string
	ClientID         string
	ClientSecret     string
	ClientAuthMethod ClientAuthMethod
}

type AuthResult struct {
	CredentialPath   string
	BaseURL          string
	UserID           string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
	HasRefreshToken  bool
}

type oauthConfig struct {
	BaseURL          string
	HTTPClient       *http.Client
	CredentialPath   string
	ClientID         string
	ClientSecret     string
	ClientAuthMethod ClientAuthMethod
}

type storedCredentials struct {
	Cached  cachedCredentials
	Path    string
	ModTime time.Time
}

type tokenResponse struct {
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

type apiKeyResponse struct {
	EncryptedAPIKey string `json:"encryptedApiKey"`
}

type tokenEndpointError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *tokenEndpointError) Error() string {
	if e == nil {
		return "model/codefree: token endpoint error"
	}
	switch {
	case strings.TrimSpace(e.Code) != "" && strings.TrimSpace(e.Description) != "":
		return fmt.Sprintf("model/codefree: token endpoint error %s: %s", strings.TrimSpace(e.Code), strings.TrimSpace(e.Description))
	case strings.TrimSpace(e.Code) != "":
		return fmt.Sprintf("model/codefree: token endpoint error %s", strings.TrimSpace(e.Code))
	case strings.TrimSpace(e.Description) != "":
		return fmt.Sprintf("model/codefree: token endpoint error: %s", strings.TrimSpace(e.Description))
	default:
		return "model/codefree: token endpoint error"
	}
}

var (
	openBrowser = defaultOpenBrowser
	authMu      sync.Mutex
	inflight    = map[string]*ensureCall{}
)

type ensureCall struct {
	done   chan struct{}
	result AuthResult
	err    error
}

func EnsureAuth(ctx context.Context, opts AuthOptions) (AuthResult, error) {
	cfg, err := resolveOAuthConfig(opts.BaseURL, opts.HTTPClient, opts.CredentialPath, opts.ClientID, opts.ClientSecret, opts.ClientAuthMethod)
	if err != nil {
		return AuthResult{}, err
	}
	if result, ok := loadExistingAuthResult(ctx, cfg); ok {
		return result, nil
	}
	return ensureAuthWithLogin(ctx, cfg, opts)
}

func EnsureModelSelectionAuth(ctx context.Context, opts AuthOptions) (bool, error) {
	cfg, err := resolveOAuthConfig(opts.BaseURL, opts.HTTPClient, opts.CredentialPath, opts.ClientID, opts.ClientSecret, opts.ClientAuthMethod)
	if err != nil {
		return false, err
	}
	if _, ok := loadExistingAuthResult(ctx, cfg); ok {
		return false, nil
	}
	if _, err := ensureAuthWithLogin(ctx, cfg, opts); err != nil {
		return false, err
	}
	return true, nil
}

func Refresh(ctx context.Context, opts RefreshOptions) (AuthResult, error) {
	cfg, err := resolveOAuthConfig(opts.BaseURL, opts.HTTPClient, opts.CredentialPath, opts.ClientID, opts.ClientSecret, opts.ClientAuthMethod)
	if err != nil {
		return AuthResult{}, err
	}
	stored, err := readStoredCredentialsAtPath(cfg.CredentialPath)
	if err != nil {
		return AuthResult{}, err
	}
	refreshed, err := refreshStoredCredentials(ctx, cfg, stored)
	if err != nil {
		return AuthResult{}, err
	}
	return authResultFromStored(refreshed), nil
}

func loadExistingAuthResult(ctx context.Context, cfg oauthConfig) (AuthResult, bool) {
	stored, err := readStoredCredentialsAtPath(cfg.CredentialPath)
	if err != nil {
		return AuthResult{}, false
	}
	if needsRefresh(stored.Cached, stored.ModTime) {
		refreshed, err := refreshStoredCredentials(ctx, cfg, stored)
		if err == nil {
			stored = refreshed
		}
	}
	if _, err := credentialsFromStored(stored); err != nil {
		return AuthResult{}, false
	}
	return authResultFromStored(stored), true
}

func ensureAuthWithLogin(ctx context.Context, cfg oauthConfig, opts AuthOptions) (AuthResult, error) {
	key := cfg.CredentialPath + "|" + cfg.BaseURL
	authMu.Lock()
	if call := inflight[key]; call != nil {
		authMu.Unlock()
		select {
		case <-ctx.Done():
			return AuthResult{}, ctx.Err()
		case <-call.done:
			return call.result, call.err
		}
	}
	call := &ensureCall{done: make(chan struct{})}
	inflight[key] = call
	authMu.Unlock()
	defer func() {
		authMu.Lock()
		delete(inflight, key)
		authMu.Unlock()
		close(call.done)
	}()
	result, err := Login(ctx, AuthOptions{
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
		return AuthResult{}, err
	}
	return result, nil
}

func Login(ctx context.Context, opts AuthOptions) (AuthResult, error) {
	cfg, err := resolveOAuthConfig(opts.BaseURL, opts.HTTPClient, opts.CredentialPath, opts.ClientID, opts.ClientSecret, opts.ClientAuthMethod)
	if err != nil {
		return AuthResult{}, err
	}
	timeout := opts.CallbackTimeout
	if timeout <= 0 {
		timeout = defaultCallbackWait
	}
	host := strings.TrimSpace(opts.RedirectHost)
	if host == "" {
		host = defaultCallbackHost
	}
	flow, err := newLoginFlowSession(host, opts.RedirectPort)
	if err != nil {
		return AuthResult{}, err
	}
	defer flow.Close()

	authURL := authorizationURL(cfg, flow.State(), flow.CodeChallenge())
	if opts.NotifyAuthURL != nil {
		opts.NotifyAuthURL(authURL)
	}
	if opts.OpenBrowser || opts.NotifyAuthURL == nil {
		if err := openBrowser(authURL); err != nil && opts.NotifyAuthURL == nil {
			return AuthResult{}, err
		}
	}
	callbackCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	callback, err := flow.Wait(callbackCtx)
	if err != nil {
		return AuthResult{}, err
	}
	if callback.Err != "" {
		return AuthResult{}, fmt.Errorf("model/codefree: oauth callback error: %s", callback.Err)
	}
	if callback.State != "" && callback.State != flow.State() {
		return AuthResult{}, fmt.Errorf("model/codefree: oauth state mismatch")
	}
	tokens, err := exchangeAuthorizationCode(ctx, cfg, flow.CodeVerifier(), callback.Code)
	if err != nil {
		return AuthResult{}, err
	}
	stored, err := persistTokenSet(ctx, cfg, cachedCredentials{}, tokens)
	if err != nil {
		return AuthResult{}, err
	}
	return authResultFromStored(stored), nil
}

func refreshStoredCredentials(ctx context.Context, cfg oauthConfig, stored storedCredentials) (storedCredentials, error) {
	refreshToken := strings.TrimSpace(stored.Cached.RefreshToken)
	if refreshToken == "" {
		return storedCredentials{}, fmt.Errorf("model/codefree: credentials %q do not contain refresh_token; relogin is required", stored.Path)
	}
	tokens, err := refreshTokenSet(ctx, cfg, refreshToken)
	if err != nil {
		var endpointErr *tokenEndpointError
		if errors.As(err, &endpointErr) && strings.EqualFold(strings.TrimSpace(endpointErr.Code), "need_not_refresh_token") {
			if expiresAt := expiresAt(stored.Cached, stored.ModTime); expiresAt.IsZero() || time.Now().Before(expiresAt) {
				return stored, nil
			}
		}
		return storedCredentials{}, err
	}
	if strings.TrimSpace(tokens.RefreshToken) == "" {
		tokens.RefreshToken = refreshToken
	}
	return persistTokenSet(ctx, cfg, stored.Cached, tokens)
}

func persistTokenSet(ctx context.Context, cfg oauthConfig, previous cachedCredentials, tokens tokenResponse) (storedCredentials, error) {
	userID := firstNonEmpty(tokens.UserID, previous.UserID)
	if userID == "" {
		return storedCredentials{}, fmt.Errorf("model/codefree: oauth response missing id_token/userId")
	}
	sessionID := firstNonEmpty(tokens.SessionID, tokens.AccessToken, previous.AccessToken)
	if sessionID == "" {
		return storedCredentials{}, fmt.Errorf("model/codefree: oauth response missing session token")
	}
	encryptedAPIKey, err := fetchEncryptedAPIKey(ctx, cfg, sessionID, userID)
	if err != nil {
		return storedCredentials{}, err
	}
	now := time.Now()
	cached := previous
	cached.AccessToken = sessionID
	if refreshToken := strings.TrimSpace(tokens.RefreshToken); refreshToken != "" {
		cached.RefreshToken = refreshToken
	}
	cached.UserID = userID
	cached.APIKey = encryptedAPIKey
	cached.BaseURL = cfg.BaseURL
	cached.TokenType = firstNonEmpty(tokens.TokenType, cached.TokenType, "bearer")
	if tokens.ExpiresIn > 0 {
		cached.ExpiresIn = tokens.ExpiresIn
		cached.ExpiresAtUnixMilli = now.Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli()
	}
	if tokens.RefreshTokenExpiresIn > 0 {
		cached.RefreshTokenExpiresIn = tokens.RefreshTokenExpiresIn
		cached.RefreshExpiresAtUnixMilli = now.Add(time.Duration(tokens.RefreshTokenExpiresIn) * time.Second).UnixMilli()
	}
	cached.ObtainedAtUnixMilli = now.UnixMilli()
	if err := saveStoredCredentials(cfg.CredentialPath, cached); err != nil {
		return storedCredentials{}, err
	}
	return readStoredCredentialsAtPath(cfg.CredentialPath)
}

func fetchEncryptedAPIKey(ctx context.Context, cfg oauthConfig, accessToken string, userID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiKeyEndpoint(cfg.BaseURL), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("sessionId", strings.TrimSpace(accessToken))
	req.Header.Set("userId", strings.TrimSpace(userID))
	req.Header.Set("projectId", "0")
	resp, err := httpClient(cfg.HTTPClient).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("model/codefree: user apikey request failed http status %d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload apiKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	value := strings.TrimSpace(payload.EncryptedAPIKey)
	if value == "" {
		return "", fmt.Errorf("model/codefree: user apikey response missing encryptedApiKey")
	}
	return value, nil
}

func exchangeAuthorizationCode(ctx context.Context, cfg oauthConfig, codeVerifier string, code string) (tokenResponse, error) {
	values := url.Values{}
	values.Set("client_id", cfg.ClientID)
	values.Set("code_verifier", strings.TrimSpace(codeVerifier))
	values.Set("code", strings.TrimSpace(code))
	values.Set("grant_type", "authorization_code")
	values.Set("redirect_uri", registeredRedirectURI(cfg.BaseURL))
	endpoint := tokenEndpoint(cfg.BaseURL) + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return tokenResponse{}, err
	}
	return doTokenRequest(req, cfg.HTTPClient)
}

func refreshTokenSet(ctx context.Context, cfg oauthConfig, refreshToken string) (tokenResponse, error) {
	values := url.Values{}
	values.Set("refresh_token", strings.TrimSpace(refreshToken))
	values.Set("client_id", cfg.ClientID)
	values.Set("grant_type", "refresh_token")
	headers := http.Header{}
	headers.Set("Content-Type", "application/x-www-form-urlencoded")
	switch cfg.ClientAuthMethod {
	case ClientAuthBasic:
		token := base64.StdEncoding.EncodeToString([]byte(cfg.ClientID + ":" + cfg.ClientSecret))
		headers.Set("Authorization", "Basic "+token)
	case ClientAuthBody:
		values.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint(cfg.BaseURL), strings.NewReader(values.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header = headers
	return doTokenRequest(req, cfg.HTTPClient)
}

func doTokenRequest(req *http.Request, client *http.Client) (tokenResponse, error) {
	resp, err := httpClient(client).Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return tokenResponse{}, readTokenEndpointError(resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return tokenResponse{}, err
	}
	payload, err := decodeTokenResponse(raw)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w; token response=%s", err, redactTokenDebug(raw))
	}
	payload.RawDebug = redactTokenDebug(raw)
	return payload, nil
}

func readTokenEndpointError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(raw))
	if values, err := url.ParseQuery(body); err == nil && (values.Get("error") != "" || values.Get("error_description") != "") {
		return &tokenEndpointError{Code: values.Get("error"), Description: values.Get("error_description")}
	}
	var payload tokenEndpointError
	if err := json.Unmarshal(raw, &payload); err == nil && (payload.Code != "" || payload.Description != "") {
		return &payload
	}
	return fmt.Errorf("model/codefree: token endpoint http status %d body=%s", resp.StatusCode, body)
}

func decodeTokenResponse(raw []byte) (tokenResponse, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return tokenResponse{}, fmt.Errorf("model/codefree: empty token response")
	}
	var payload tokenResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		if values, parseErr := url.ParseQuery(strings.TrimSpace(string(raw))); parseErr == nil {
			payload.AccessToken = firstNonEmpty(values.Get("access_token"), values.Get("ori_session_id"))
			payload.RefreshToken = values.Get("refresh_token")
			payload.TokenType = values.Get("token_type")
			payload.UserID = firstNonEmpty(values.Get("id_token"), values.Get("userId"), values.Get("user_id"), values.Get("uid"))
			payload.SessionID = values.Get("ori_session_id")
			payload.OriginalToken = values.Get("ori_token")
			payload.ExpiresIn = parseInt64(values.Get("expires_in"))
			payload.RefreshTokenExpiresIn = parseInt64(values.Get("refresh_token_expires_in"))
		}
	}
	payload.UserID = firstNonEmpty(payload.UserID, extractJSONTokenField(raw, "userId"), extractJSONTokenField(raw, "user_id"), extractJSONTokenField(raw, "uid"))
	payload.SessionID = firstNonEmpty(payload.SessionID, extractJSONTokenField(raw, "ori_session_id"), extractJSONTokenField(raw, "session_id"))
	payload.OriginalToken = firstNonEmpty(payload.OriginalToken, extractJSONTokenField(raw, "ori_token"))
	if payload.AccessToken == "" && payload.SessionID == "" && payload.OriginalToken == "" && payload.UserID == "" {
		return tokenResponse{}, fmt.Errorf("model/codefree: unsupported token response format")
	}
	return payload, nil
}

func resolveOAuthConfig(baseURL string, client *http.Client, credentialPath string, clientID string, clientSecret string, authMethod ClientAuthMethod) (oauthConfig, error) {
	path, err := resolveCredentialPath(credentialPath)
	if err != nil {
		return oauthConfig{}, err
	}
	secret := firstNonEmpty(clientSecret, os.Getenv(clientSecretEnv))
	resolved := oauthConfig{
		BaseURL:          normalizeBaseURL(baseURL),
		HTTPClient:       client,
		CredentialPath:   path,
		ClientID:         firstNonEmpty(clientID, os.Getenv(clientIDEnv), defaultOAuthClientID),
		ClientSecret:     secret,
		ClientAuthMethod: normalizeClientAuthMethod(authMethod, secret),
	}
	if resolved.ClientID == "" {
		return oauthConfig{}, fmt.Errorf("model/codefree: oauth client id is empty")
	}
	return resolved, nil
}

func normalizeClientAuthMethod(method ClientAuthMethod, clientSecret string) ClientAuthMethod {
	if method == "" {
		method = ClientAuthMethod(strings.TrimSpace(os.Getenv(clientAuthMethodEnv)))
	}
	switch strings.ToLower(strings.TrimSpace(string(method))) {
	case "", string(ClientAuthBody):
		if strings.TrimSpace(clientSecret) == "" {
			return ClientAuthNone
		}
		return ClientAuthBody
	case string(ClientAuthBasic):
		return ClientAuthBasic
	case string(ClientAuthNone):
		return ClientAuthNone
	default:
		return ClientAuthBody
	}
}

func credentialsFromStored(stored storedCredentials) (credentials, error) {
	userID := strings.TrimSpace(stored.Cached.UserID)
	if userID == "" {
		return credentials{}, fmt.Errorf("model/codefree: credentials missing id_token")
	}
	apiKey, err := decryptAPIKey(stored.Cached.APIKey)
	if err != nil {
		return credentials{}, err
	}
	return credentials{UserID: userID, APIKey: apiKey, Path: stored.Path}, nil
}

func saveStoredCredentials(path string, cached cachedCredentials) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("model/codefree: ensure credential dir %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return fmt.Errorf("model/codefree: encode credentials: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("model/codefree: write credentials %q: %w", path, err)
	}
	return nil
}

func readStoredCredentialsAtPath(path string) (storedCredentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return storedCredentials{}, fmt.Errorf("model/codefree: read credentials %q: %w", path, err)
	}
	var cached cachedCredentials
	if err := json.Unmarshal(raw, &cached); err != nil {
		return storedCredentials{}, fmt.Errorf("model/codefree: decode credentials %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return storedCredentials{}, fmt.Errorf("model/codefree: stat credentials %q: %w", path, err)
	}
	return storedCredentials{Cached: cached, Path: path, ModTime: info.ModTime()}, nil
}

func authResultFromStored(stored storedCredentials) AuthResult {
	return AuthResult{
		CredentialPath:   stored.Path,
		BaseURL:          firstNonEmpty(stored.Cached.BaseURL, defaultBaseURL),
		UserID:           strings.TrimSpace(stored.Cached.UserID),
		ExpiresAt:        expiresAt(stored.Cached, stored.ModTime),
		RefreshExpiresAt: refreshExpiresAt(stored.Cached, stored.ModTime),
		HasRefreshToken:  strings.TrimSpace(stored.Cached.RefreshToken) != "",
	}
}

func needsRefresh(cached cachedCredentials, modTime time.Time) bool {
	if strings.TrimSpace(cached.RefreshToken) == "" {
		return false
	}
	if strings.TrimSpace(cached.UserID) == "" || strings.TrimSpace(cached.APIKey) == "" {
		return true
	}
	expiresAt := expiresAt(cached, modTime)
	return !expiresAt.IsZero() && !time.Now().Before(expiresAt)
}

func expiresAt(cached cachedCredentials, modTime time.Time) time.Time {
	if cached.ExpiresAtUnixMilli > 0 {
		return time.UnixMilli(cached.ExpiresAtUnixMilli)
	}
	if cached.ObtainedAtUnixMilli > 0 && cached.ExpiresIn > 0 {
		return time.UnixMilli(cached.ObtainedAtUnixMilli).Add(time.Duration(cached.ExpiresIn) * time.Second)
	}
	if !modTime.IsZero() && cached.ExpiresIn > 0 {
		return modTime.Add(time.Duration(cached.ExpiresIn) * time.Second)
	}
	return time.Time{}
}

func refreshExpiresAt(cached cachedCredentials, modTime time.Time) time.Time {
	if cached.RefreshExpiresAtUnixMilli > 0 {
		return time.UnixMilli(cached.RefreshExpiresAtUnixMilli)
	}
	if cached.ObtainedAtUnixMilli > 0 && cached.RefreshTokenExpiresIn > 0 {
		return time.UnixMilli(cached.ObtainedAtUnixMilli).Add(time.Duration(cached.RefreshTokenExpiresIn) * time.Second)
	}
	if !modTime.IsZero() && cached.RefreshTokenExpiresIn > 0 {
		return modTime.Add(time.Duration(cached.RefreshTokenExpiresIn) * time.Second)
	}
	return time.Time{}
}

func authorizationURL(cfg oauthConfig, state string, codeChallenge string) string {
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", cfg.ClientID)
	values.Set("redirect_uri", registeredRedirectURI(cfg.BaseURL))
	values.Set("code_challenge", codeChallenge)
	values.Set("code_challenge_method", "S256")
	values.Set("state", state)
	return authorizeEndpoint(cfg.BaseURL) + "?" + values.Encode()
}

type oauthCallback struct {
	Code  string
	State string
	Err   string
}

type loginFlowSession interface {
	State() string
	CodeChallenge() string
	CodeVerifier() string
	Wait(context.Context) (oauthCallback, error)
	Close() error
}

var newLoginFlowSession = func(host string, port int) (loginFlowSession, error) {
	return newLoginFlow(host, port)
}

type loginFlow struct {
	listener      net.Listener
	server        *http.Server
	events        chan oauthCallback
	state         string
	codeVerifier  string
	codeChallenge string
	localPath     string
}

func newLoginFlow(host string, port int) (*loginFlow, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("model/codefree: listen for oauth callback: %w", err)
	}
	callbackID, err := randomDigits(4)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	codeVerifier, err := randomURLSafe(48)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	localPath := "/" + callbackID
	localURL := "http://" + listener.Addr().String() + localPath
	flow := &loginFlow{
		listener:      listener,
		events:        make(chan oauthCallback, 1),
		state:         base64.StdEncoding.EncodeToString([]byte(localURL)),
		codeVerifier:  codeVerifier,
		codeChallenge: s256(codeVerifier),
		localPath:     localPath,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.URL.Path) != flow.localPath {
			http.NotFound(w, r)
			return
		}
		callback := oauthCallback{
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

func (f *loginFlow) Wait(ctx context.Context) (oauthCallback, error) {
	if f == nil {
		return oauthCallback{}, fmt.Errorf("model/codefree: oauth flow is nil")
	}
	select {
	case callback := <-f.events:
		if callback.Code == "" && callback.Err == "" {
			return oauthCallback{}, fmt.Errorf("model/codefree: oauth callback missing code")
		}
		return callback, nil
	case <-ctx.Done():
		return oauthCallback{}, fmt.Errorf("model/codefree: wait for oauth callback: %w", ctx.Err())
	}
}

func (f *loginFlow) Close() error {
	if f == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = f.server.Shutdown(ctx)
	return f.listener.Close()
}

func (f *loginFlow) State() string {
	if f == nil {
		return ""
	}
	return f.state
}

func (f *loginFlow) CodeChallenge() string {
	if f == nil {
		return ""
	}
	return f.codeChallenge
}

func (f *loginFlow) CodeVerifier() string {
	if f == nil {
		return ""
	}
	return f.codeVerifier
}

func defaultOpenBrowser(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("model/codefree: oauth url is empty")
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
		return fmt.Errorf("model/codefree: open browser for oauth: %w", err)
	}
	return nil
}

func randomURLSafe(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("model/codefree: generate oauth random value: %w", err)
	}
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(buf), "="), nil
}

func randomDigits(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("model/codefree: invalid callback digit length")
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("model/codefree: generate callback id: %w", err)
	}
	out := make([]byte, length)
	for i := range buf {
		out[i] = '0' + (buf[i] % 10)
	}
	return string(out), nil
}

func s256(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(sum[:]), "=")
}

func authorizeEndpoint(baseURL string) string {
	return normalizeBaseURL(baseURL) + oauthAuthorizePath
}

func tokenEndpoint(baseURL string) string {
	return normalizeBaseURL(baseURL) + oauthTokenPath
}

func apiKeyEndpoint(baseURL string) string {
	return normalizeBaseURL(baseURL) + userAPIKeyPath
}

func registeredRedirectURI(baseURL string) string {
	return normalizeBaseURL(baseURL) + oauthRedirectPath
}

func normalizeBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(firstNonEmpty(baseURL, defaultBaseURL)), "/")
}

func httpClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 90 * time.Second}
}

func parseInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed
}

func extractJSONTokenField(raw []byte, key string) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func redactTokenDebug(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	return "<redacted>"
}
