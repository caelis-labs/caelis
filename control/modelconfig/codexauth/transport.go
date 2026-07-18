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

func (t *authenticatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("codexauth: request is nil")
	}
	if !allowedCodexRequest(request) {
		return nil, fmt.Errorf("codexauth: refusing to send OAuth credentials outside maintained ChatGPT endpoints")
	}
	credentials, err := t.manager.accessToken(request.Context(), nil)
	if err != nil {
		if errors.Is(err, ErrNoCredentials) || errors.Is(err, ErrReauthenticationRequired) {
			return nil, &terminalAuthenticationError{cause: err}
		}
		return nil, err
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+credentials.token)
	clone.Header.Set("ChatGPT-Account-ID", credentials.accountID)
	response, err := t.base.RoundTrip(clone)
	if err == nil && response != nil && response.StatusCode == http.StatusUnauthorized {
		// Do not replay a possibly non-idempotent request. Clear only the token
		// used by this request so the next request performs one normal refresh.
		t.manager.invalidateAccess(credentials.token)
	}
	return response, err
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
