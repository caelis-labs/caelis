package grokauth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

type authenticatedTransport struct {
	manager *Manager
	base    http.RoundTripper
}

func (t *authenticatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("grokauth: request is nil")
	}
	if !allowedXAIRequest(request) {
		return nil, fmt.Errorf("grokauth: refusing to send OAuth credentials outside the maintained Grok Build API")
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
	clone.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	clone.Header.Set("x-grok-client-version", grokBuildProtocolVersion)
	clone.Header.Set("x-grok-client-identifier", "caelis")
	response, err := t.base.RoundTrip(clone)
	if err == nil && response != nil && response.StatusCode == http.StatusUnauthorized {
		// Do not replay a potentially non-idempotent generation request. The
		// next request performs one normal refresh under the manager lock.
		t.manager.invalidateAccess(credentials.token)
	}
	return response, err
}

func allowedXAIRequest(request *http.Request) bool {
	if request == nil || request.URL == nil {
		return false
	}
	if !strings.EqualFold(request.URL.Scheme, "https") || !strings.EqualFold(request.URL.Hostname(), "cli-chat-proxy.grok.com") {
		return false
	}
	if port := request.URL.Port(); port != "" && port != "443" {
		return false
	}
	path := request.URL.EscapedPath()
	return path == "/v1" || strings.HasPrefix(path, "/v1/")
}

type terminalAuthenticationError struct {
	cause error
}

func (e *terminalAuthenticationError) Error() string {
	if e == nil || e.cause == nil {
		return "grok oauth authentication failed"
	}
	return e.cause.Error()
}

func (e *terminalAuthenticationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *terminalAuthenticationError) Retryable() bool { return false }

func (e *terminalAuthenticationError) ErrorCode() errorcode.Code {
	return errorcode.Unauthenticated
}
