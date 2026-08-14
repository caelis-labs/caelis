package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	Error            any    `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type refreshRequest struct {
	ClientID     string `json:"client_id"`
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
}

type jwtClaims struct {
	ExpiresAt     int64  `json:"exp"`
	AccountID     string `json:"chatgpt_account_id"`
	Organizations []struct {
		ID string `json:"id"`
	} `json:"organizations"`
	Auth struct {
		AccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

func (m *Manager) refreshLocked(ctx context.Context, client *http.Client) error {
	payload := refreshRequest{
		ClientID:     ClientID,
		GrantType:    "refresh_token",
		RefreshToken: m.stored.RefreshToken,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("codexauth: encode refresh request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.issuer+"/oauth/token", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("codexauth: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("codexauth: refresh access token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return readTokenEndpointError("refresh access token", response)
	}
	tokens, err := decodeTokenResponse(response.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return fmt.Errorf("codexauth: refresh response omitted access token")
	}
	accountID := strings.TrimSpace(m.stored.AccountID)
	if accountID == "" {
		return fmt.Errorf("codexauth: refreshed token omitted ChatGPT account identity")
	}
	if claimedAccountID := firstAccountID(tokens.IDToken, tokens.AccessToken); claimedAccountID != "" && claimedAccountID != accountID {
		return fmt.Errorf("codexauth: refreshed token changed ChatGPT account identity: %w", ErrReauthenticationRequired)
	}
	refreshToken := strings.TrimSpace(tokens.RefreshToken)
	if refreshToken == "" {
		refreshToken = m.stored.RefreshToken
	}
	expiresAt := tokenExpiry(tokens.AccessToken, tokens.ExpiresIn, m.now())
	stored := storedCredentials{
		Version:      credentialSchemaVersion,
		RefreshToken: refreshToken,
		AccountID:    accountID,
		AccessToken:  strings.TrimSpace(tokens.AccessToken),
		ExpiresAt:    expiresAt.Unix(),
	}
	if err := writeStoredCredentials(m.credentialPath, stored); err != nil {
		return err
	}
	m.stored = stored
	m.loaded = true
	m.access = accessCredentials{
		token:     strings.TrimSpace(tokens.AccessToken),
		accountID: accountID,
		expiresAt: expiresAt,
	}
	m.rejectedAccessToken = ""
	return nil
}

func (m *Manager) exchangeCode(ctx context.Context, client *http.Client, code string, verifier string, redirectURI string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", strings.TrimSpace(redirectURI))
	form.Set("client_id", ClientID)
	form.Set("code_verifier", verifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.issuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("codexauth: build authorization-code exchange: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("codexauth: exchange authorization code: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenResponse{}, readTokenEndpointError("exchange authorization code", response)
	}
	tokens, err := decodeTokenResponse(response.Body)
	if err != nil {
		return tokenResponse{}, err
	}
	if strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" {
		return tokenResponse{}, fmt.Errorf("codexauth: authorization response omitted required tokens")
	}
	return tokens, nil
}

func decodeTokenResponse(reader io.Reader) (tokenResponse, error) {
	var response tokenResponse
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	if err := decoder.Decode(&response); err != nil {
		return tokenResponse{}, fmt.Errorf("codexauth: decode token response: %w", err)
	}
	return response, nil
}

func readTokenEndpointError(operation string, response *http.Response) error {
	var detail tokenEndpointError
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&detail)
	code, nestedDescription := tokenEndpointErrorFields(detail.Error)
	description := sanitizeOAuthError(detail.ErrorDescription)
	if description == "" {
		description = nestedDescription
	}
	if response.StatusCode == http.StatusUnauthorized || strings.Contains(code, "invalid_grant") || strings.Contains(code, "refresh_token") {
		return fmt.Errorf("codexauth: %s: %w", operation, ErrReauthenticationRequired)
	}
	if code == "" {
		code = "oauth_error"
	}
	if description != "" {
		return fmt.Errorf("codexauth: %s failed with status %d (%s: %s)", operation, response.StatusCode, code, description)
	}
	return fmt.Errorf("codexauth: %s failed with status %d (%s)", operation, response.StatusCode, code)
}

func tokenEndpointErrorFields(value any) (string, string) {
	switch value := value.(type) {
	case string:
		return sanitizeOAuthError(value), ""
	case map[string]any:
		code, _ := value["code"].(string)
		message, _ := value["message"].(string)
		return sanitizeOAuthError(code), sanitizeOAuthError(message)
	default:
		return "", ""
	}
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

func firstAccountID(tokens ...string) string {
	for _, token := range tokens {
		if claims, err := decodeJWTClaims(token); err == nil {
			if accountID := strings.TrimSpace(claims.Auth.AccountID); accountID != "" {
				return accountID
			}
			if accountID := strings.TrimSpace(claims.AccountID); accountID != "" {
				return accountID
			}
			if len(claims.Organizations) > 0 {
				if accountID := strings.TrimSpace(claims.Organizations[0].ID); accountID != "" {
					return accountID
				}
			}
		}
	}
	return ""
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
