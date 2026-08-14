package codexauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

// Keep the third-party flow least-privileged: Caelis uses only Codex model
// inference and does not request the official client's connector scopes.
const oauthScope = "openid profile email offline_access"

type callbackResult struct {
	code string
	err  error
}

func (m *Manager) login(ctx context.Context, opts LoginOptions, launchBrowser bool) error {
	state, err := randomURLSafe(m.random, 32)
	if err != nil {
		return fmt.Errorf("codexauth: generate oauth state: %w", err)
	}
	verifier, err := randomURLSafe(m.random, 32)
	if err != nil {
		return fmt.Errorf("codexauth: generate PKCE verifier: %w", err)
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	authorizationURL := m.authorizationURL(state, challenge)

	listener, err := m.listen("tcp", callbackAddress)
	if err != nil {
		return fmt.Errorf("codexauth: listen for oauth callback on %s: %w", callbackAddress, errors.Join(errBrowserLoginUnavailable, err))
	}
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

	if launchBrowser {
		modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
			Provider: "openai-codex", Phase: modelconfig.AuthProgressOpeningBrowser, VerificationURL: authorizationURL,
		})
		if err := m.browserOpener(authorizationURL); err != nil {
			return fmt.Errorf("codexauth: open browser: %w", errors.Join(errBrowserLoginUnavailable, err))
		}
	}
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider: "openai-codex", Phase: modelconfig.AuthProgressWaitingForBrowser, VerificationURL: authorizationURL,
	})
	waitCtx := ctx
	if opts.CallbackTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, opts.CallbackTimeout)
		defer cancel()
	}
	var result callbackResult
	select {
	case <-waitCtx.Done():
		return fmt.Errorf("codexauth: wait for oauth callback: %w", waitCtx.Err())
	case err := <-serveErrors:
		return fmt.Errorf("codexauth: serve oauth callback: %w", err)
	case result = <-results:
	}
	if result.err != nil {
		return result.err
	}
	client := opts.HTTPClient
	if client == nil {
		client = m.httpClient
	}
	tokens, err := m.exchangeCode(ctx, client, result.code, verifier, RedirectURI)
	if err != nil {
		return err
	}
	return m.installLoginTokens(ctx, tokens)
}

func (m *Manager) installLoginTokens(ctx context.Context, tokens tokenResponse) error {
	accountID := firstAccountID(tokens.IDToken, tokens.AccessToken)
	if accountID == "" {
		return fmt.Errorf("codexauth: authorization tokens omitted ChatGPT account identity")
	}
	expiresAt := tokenExpiry(tokens.AccessToken, tokens.ExpiresIn, m.now())
	stored := storedCredentials{
		Version:      credentialSchemaVersion,
		RefreshToken: strings.TrimSpace(tokens.RefreshToken),
		AccountID:    accountID,
		AccessToken:  strings.TrimSpace(tokens.AccessToken),
		ExpiresAt:    expiresAt.Unix(),
	}
	if err := ensureCredentialDirectory(m.credentialPath); err != nil {
		return err
	}
	lock, err := acquireCredentialFileLock(ctx, m.credentialPath+".lock")
	if err != nil {
		return fmt.Errorf("codexauth: acquire credential login lock: %w", err)
	}
	writeErr := writeStoredCredentials(m.credentialPath, stored)
	closeErr := lock.Close()
	if writeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("codexauth: release credential login lock: %w", closeErr)
	}
	// Install in memory only after the atomic write and lock release. A failed
	// persistence operation must not leave this process using credentials that
	// sibling Caelis processes cannot observe.
	m.mu.Lock()
	m.loaded = true
	m.stored = stored
	m.access = accessCredentials{
		token:     strings.TrimSpace(tokens.AccessToken),
		accountID: accountID,
		expiresAt: expiresAt,
	}
	m.rejectedAccessToken = ""
	m.mu.Unlock()
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider: "openai-codex", Phase: modelconfig.AuthProgressAuthenticated,
	})
	return nil
}

func (m *Manager) authorizationURL(state string, challenge string) string {
	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", ClientID)
	query.Set("redirect_uri", RedirectURI)
	query.Set("scope", oauthScope)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("state", state)
	query.Set("originator", "caelis")
	return m.issuer + "/oauth/authorize?" + query.Encode()
}

func callbackServer(expectedState string, results chan<- callbackResult) *http.Server {
	mux := http.NewServeMux()
	var once sync.Once
	mux.HandleFunc("/auth/callback", func(writer http.ResponseWriter, request *http.Request) {
		result := parseCallback(request, expectedState)
		status := http.StatusOK
		title := "Codex sign-in complete"
		message := "You can close this window and return to Caelis."
		if result.err != nil {
			status = http.StatusBadRequest
			title = "Codex sign-in failed"
			message = result.err.Error()
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(status)
		_, _ = fmt.Fprintf(writer, "<!doctype html><html><body><h1>%s</h1><p>%s</p></body></html>", html.EscapeString(title), html.EscapeString(message))
		once.Do(func() { results <- result })
	})
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func parseCallback(request *http.Request, expectedState string) callbackResult {
	query := request.URL.Query()
	actualState := query.Get("state")
	if len(actualState) != len(expectedState) || subtle.ConstantTimeCompare([]byte(actualState), []byte(expectedState)) != 1 {
		return callbackResult{err: fmt.Errorf("codexauth: oauth state mismatch")}
	}
	if code := sanitizeOAuthError(query.Get("error")); code != "" {
		description := sanitizeOAuthError(query.Get("error_description"))
		if description == "" {
			description = code
		}
		return callbackResult{err: fmt.Errorf("codexauth: oauth authorization failed (%s)", description)}
	}
	code := strings.TrimSpace(query.Get("code"))
	if code == "" {
		return callbackResult{err: fmt.Errorf("codexauth: oauth callback omitted authorization code")}
	}
	return callbackResult{code: code}
}

func randomURLSafe(reader interface{ Read([]byte) (int, error) }, byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
