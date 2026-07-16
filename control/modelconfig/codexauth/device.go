package codexauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

type deviceCodeResponse struct {
	DeviceAuthID   string          `json:"device_auth_id"`
	UserCode       string          `json:"user_code"`
	LegacyUserCode string          `json:"usercode"`
	Interval       json.RawMessage `json:"interval"`
}

type deviceAuthorizationResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeChallenge     string `json:"code_challenge"`
	CodeVerifier      string `json:"code_verifier"`
}

func (m *Manager) loginWithDeviceCode(ctx context.Context, opts LoginOptions) error {
	client := opts.HTTPClient
	if client == nil {
		client = m.httpClient
	}
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider: "openai-codex", Phase: modelconfig.AuthProgressRequestingDeviceCode,
	})
	deviceCode, err := m.requestDeviceCode(ctx, client)
	if err != nil {
		return err
	}
	verificationURL := m.issuer + "/codex/device"
	modelconfig.ReportAuthProgress(ctx, modelconfig.AuthProgress{
		Provider:        "openai-codex",
		Phase:           modelconfig.AuthProgressWaitingForDevice,
		VerificationURL: verificationURL,
		UserCode:        deviceCode.UserCode,
	})
	authorization, err := m.pollDeviceAuthorization(ctx, client, deviceCode)
	if err != nil {
		return err
	}
	verifier := strings.TrimSpace(authorization.CodeVerifier)
	if verifier == "" || strings.TrimSpace(authorization.AuthorizationCode) == "" {
		return fmt.Errorf("codexauth: device authorization omitted PKCE exchange fields")
	}
	if challenge := strings.TrimSpace(authorization.CodeChallenge); challenge != "" {
		digest := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
			return fmt.Errorf("codexauth: device authorization returned an invalid PKCE challenge")
		}
	}
	tokens, err := m.exchangeCode(
		ctx,
		client,
		authorization.AuthorizationCode,
		verifier,
		m.issuer+"/deviceauth/callback",
	)
	if err != nil {
		return err
	}
	return m.installLoginTokens(ctx, tokens)
}

func (m *Manager) requestDeviceCode(ctx context.Context, client *http.Client) (deviceCodeResponse, error) {
	payload := struct {
		ClientID string `json:"client_id"`
	}{ClientID: ClientID}
	response, err := doDeviceJSON(ctx, client, m.issuer+"/api/accounts/deviceauth/usercode", payload)
	if err != nil {
		return deviceCodeResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return deviceCodeResponse{}, fmt.Errorf("codexauth: %w", ErrDeviceCodeUnavailable)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return deviceCodeResponse{}, fmt.Errorf("codexauth: request device code failed with status %d", response.StatusCode)
	}
	var result deviceCodeResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return deviceCodeResponse{}, fmt.Errorf("codexauth: decode device code response: %w", err)
	}
	result.DeviceAuthID = strings.TrimSpace(result.DeviceAuthID)
	result.UserCode = strings.TrimSpace(result.UserCode)
	if result.UserCode == "" {
		result.UserCode = strings.TrimSpace(result.LegacyUserCode)
	}
	if result.DeviceAuthID == "" || result.UserCode == "" {
		return deviceCodeResponse{}, fmt.Errorf("codexauth: device code response omitted required fields")
	}
	return result, nil
}

func (m *Manager) pollDeviceAuthorization(ctx context.Context, client *http.Client, deviceCode deviceCodeResponse) (deviceAuthorizationResponse, error) {
	pollCtx, cancel := context.WithTimeout(ctx, deviceCodeTTL)
	defer cancel()
	interval := devicePollInterval(deviceCode.Interval)
	payload := struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
	}{DeviceAuthID: deviceCode.DeviceAuthID, UserCode: deviceCode.UserCode}
	for {
		response, err := doDeviceJSON(pollCtx, client, m.issuer+"/api/accounts/deviceauth/token", payload)
		if err != nil {
			return deviceAuthorizationResponse{}, err
		}
		status := response.StatusCode
		if status >= 200 && status < 300 {
			var result deviceAuthorizationResponse
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
			closeErr := response.Body.Close()
			if decodeErr != nil {
				return deviceAuthorizationResponse{}, fmt.Errorf("codexauth: decode device authorization response: %w", decodeErr)
			}
			if closeErr != nil {
				return deviceAuthorizationResponse{}, fmt.Errorf("codexauth: close device authorization response: %w", closeErr)
			}
			return result, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		closeErr := response.Body.Close()
		if closeErr != nil {
			return deviceAuthorizationResponse{}, fmt.Errorf("codexauth: close device authorization response: %w", closeErr)
		}
		if status != http.StatusForbidden && status != http.StatusNotFound {
			return deviceAuthorizationResponse{}, fmt.Errorf("codexauth: device authorization failed with status %d", status)
		}
		timer := time.NewTimer(interval)
		select {
		case <-pollCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if errors.Is(pollCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				return deviceAuthorizationResponse{}, fmt.Errorf("codexauth: device authorization timed out after 15 minutes")
			}
			return deviceAuthorizationResponse{}, pollCtx.Err()
		case <-timer.C:
		}
	}
}

func doDeviceJSON(ctx context.Context, client *http.Client, endpoint string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("codexauth: encode device authorization request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("codexauth: build device authorization request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "caelis")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("codexauth: device authorization request: %w", err)
	}
	return response, nil
}

func devicePollInterval(raw json.RawMessage) time.Duration {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		seconds = 5
	}
	if seconds > 30 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}
