package codexauth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	codexBackendPath = "/backend-api/codex"
	codexUsagePath   = "/backend-api/wham/usage"
)

type authenticatedTransport struct {
	manager *Manager
	base    http.RoundTripper
}

func (t *authenticatedTransport) CloseIdleConnections() {
	if t == nil || t.base == nil {
		return
	}
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t *authenticatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("codexauth: request is nil")
	}
	if !allowedCodexRequest(request) {
		return nil, fmt.Errorf("codexauth: refusing to send OAuth credentials outside maintained ChatGPT endpoints")
	}
	credentials, err := t.accessCredentials(request)
	if err != nil {
		return nil, err
	}
	response, err := t.base.RoundTrip(authenticatedRequest(request, credentials))
	if err != nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}

	// A backend can reject an access token before its local expiry. Requests
	// created by the maintained Codex provider are replayable, so refresh once
	// and hide that stale-token boundary from the caller. Never retry a body the
	// caller cannot recreate, and never retry a second 401.
	t.manager.invalidateAccess(credentials.token)
	if !requestReplayable(request) {
		return response, nil
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	credentials, err = t.accessCredentials(request)
	if err != nil {
		return nil, err
	}
	retry, err := replayAuthenticatedRequest(request, credentials)
	if err != nil {
		return nil, err
	}
	response, err = t.base.RoundTrip(retry)
	if err == nil && response != nil && response.StatusCode == http.StatusUnauthorized {
		t.manager.invalidateAccess(credentials.token)
	}
	return response, err
}

func (t *authenticatedTransport) accessCredentials(request *http.Request) (accessCredentials, error) {
	credentials, err := t.manager.accessToken(request.Context(), nil)
	if err != nil {
		if errors.Is(err, ErrNoCredentials) || errors.Is(err, ErrReauthenticationRequired) {
			return accessCredentials{}, &terminalAuthenticationError{cause: err}
		}
		return accessCredentials{}, err
	}
	return credentials, nil
}

func authenticatedRequest(request *http.Request, credentials accessCredentials) *http.Request {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+credentials.token)
	clone.Header.Set("ChatGPT-Account-ID", credentials.accountID)
	return clone
}

func replayAuthenticatedRequest(request *http.Request, credentials accessCredentials) (*http.Request, error) {
	clone := authenticatedRequest(request, credentials)
	if request.Body == nil || request.Body == http.NoBody {
		return clone, nil
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, fmt.Errorf("codexauth: recreate request body after access token refresh: %w", err)
	}
	clone.Body = body
	return clone, nil
}

func requestReplayable(request *http.Request) bool {
	return request.Body == nil || request.Body == http.NoBody || request.GetBody != nil
}

func allowedCodexRequest(request *http.Request) bool {
	if request == nil || request.URL == nil {
		return false
	}
	if !strings.EqualFold(request.URL.Scheme, "https") || !strings.EqualFold(request.URL.Hostname(), "chatgpt.com") {
		return false
	}
	if port := request.URL.Port(); port != "" && port != "443" {
		return false
	}
	path := request.URL.EscapedPath()
	return path == codexUsagePath || path == codexBackendPath || strings.HasPrefix(path, codexBackendPath+"/")
}
