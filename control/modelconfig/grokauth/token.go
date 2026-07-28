package grokauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type tokenEndpointError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type jwtClaims struct {
	ExpiresAt     int64  `json:"exp"`
	Subject       string `json:"sub"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
}

func (m *Manager) refreshLocked(ctx context.Context, client *http.Client) error {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", m.stored.RefreshToken)
	form.Set("client_id", ClientID)
	tokens, err := m.requestTokens(ctx, client, "refresh access token", form)
	if err != nil {
		return err
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return fmt.Errorf("grokauth: refresh response omitted access token")
	}
	refreshToken := strings.TrimSpace(tokens.RefreshToken)
	if refreshToken == "" {
		refreshToken = m.stored.RefreshToken
	}
	expiresAt := tokenExpiry(tokens.AccessToken, tokens.ExpiresIn, m.now())
	stored := storedCredentials{
		Version:      credentialSchemaVersion,
		RefreshToken: refreshToken,
		AccessToken:  strings.TrimSpace(tokens.AccessToken),
		ExpiresAt:    expiresAt.Unix(),
	}
	if err := writeStoredCredentials(m.credentialPath, stored); err != nil {
		return err
	}
	m.stored = stored
	m.loaded = true
	m.access = accessCredentials{token: stored.AccessToken, expiresAt: expiresAt}
	m.rejectedAccessToken = ""
	return nil
}

func (m *Manager) exchangeCode(ctx context.Context, client *http.Client, code string, verifier string, redirectURI string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", strings.TrimSpace(redirectURI))
	form.Set("client_id", ClientID)
	form.Set("code_verifier", strings.TrimSpace(verifier))
	tokens, err := m.requestTokens(ctx, client, "exchange authorization code", form)
	if err != nil {
		return tokenResponse{}, err
	}
	if strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" {
		return tokenResponse{}, fmt.Errorf("grokauth: authorization response omitted required tokens")
	}
	return tokens, nil
}

func (m *Manager) requestTokens(ctx context.Context, client *http.Client, operation string, form url.Values) (tokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.issuer+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("grokauth: build %s request: %w", operation, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "caelis")
	response, err := client.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("grokauth: %s: %w", operation, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return tokenResponse{}, readTokenEndpointError(operation, response)
	}
	var tokens tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokens); err != nil {
		return tokenResponse{}, fmt.Errorf("grokauth: decode token response: %w", err)
	}
	return tokens, nil
}

func (m *Manager) installLoginTokens(ctx context.Context, tokens tokenResponse) error {
	expiresAt := tokenExpiry(tokens.AccessToken, tokens.ExpiresIn, m.now())
	stored := storedCredentials{
		Version:      credentialSchemaVersion,
		RefreshToken: strings.TrimSpace(tokens.RefreshToken),
		AccessToken:  strings.TrimSpace(tokens.AccessToken),
		ExpiresAt:    expiresAt.Unix(),
	}
	if !stored.valid() || stored.AccessToken == "" {
		return fmt.Errorf("grokauth: authorization response omitted required tokens")
	}
	if err := ensureCredentialDirectory(m.credentialPath); err != nil {
		return err
	}
	lock, err := acquireCredentialFileLock(ctx, m.credentialPath+".lock")
	if err != nil {
		return fmt.Errorf("grokauth: acquire credential login lock: %w", err)
	}
	writeErr := writeStoredCredentials(m.credentialPath, stored)
	closeErr := lock.Close()
	if writeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("grokauth: release credential login lock: %w", closeErr)
	}
	m.mu.Lock()
	m.loaded = true
	m.stored = stored
	m.access = accessCredentials{token: stored.AccessToken, expiresAt: expiresAt}
	m.rejectedAccessToken = ""
	m.mu.Unlock()
	return nil
}

func readTokenEndpointError(operation string, response *http.Response) error {
	var detail tokenEndpointError
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&detail)
	code := sanitizeOAuthError(detail.Error)
	description := sanitizeOAuthError(detail.ErrorDescription)
	if response.StatusCode == http.StatusUnauthorized || code == "invalid_grant" || code == "invalid_client" {
		return fmt.Errorf("grokauth: %s: %w", operation, ErrReauthenticationRequired)
	}
	if code == "" {
		code = "oauth_error"
	}
	if description != "" {
		return fmt.Errorf("grokauth: %s failed with status %d (%s: %s)", operation, response.StatusCode, code, description)
	}
	return fmt.Errorf("grokauth: %s failed with status %d (%s)", operation, response.StatusCode, code)
}

func sanitizeOAuthError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			return ' '
		}
		return r
	}, value)
	return strings.TrimSpace(value)
}

func tokenExpiry(accessToken string, expiresIn int64, now time.Time) time.Time {
	if claims, err := decodeJWTClaims(accessToken); err == nil && claims.ExpiresAt > 0 {
		return time.Unix(claims.ExpiresAt, 0)
	}
	if expiresIn > 0 {
		return now.Add(time.Duration(expiresIn) * time.Second)
	}
	return now.Add(defaultLifetime)
}

func decodeJWTClaims(token string) (jwtClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[1] == "" {
		return jwtClaims{}, fmt.Errorf("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, err
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return jwtClaims{}, err
	}
	return claims, nil
}
