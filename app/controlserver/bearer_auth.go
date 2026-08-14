package controlserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// BearerTokenAuthenticator constructs a static-token HTTP authentication
// boundary. The principal is trusted server configuration and is never read
// from client-supplied request data.
func BearerTokenAuthenticator(token string, principal controlclient.Principal) (Authenticator, error) {
	token = strings.TrimSpace(token)
	principal.ID = strings.TrimSpace(principal.ID)
	if len(token) < sha256.Size || principal.ID == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("controlserver: bearer token and principal are required")
	}
	expected := sha256.Sum256([]byte(token))
	return AuthenticatorFunc(func(request *http.Request) (controlclient.Principal, error) {
		values := request.Header.Values("Authorization")
		if len(values) != 1 || strings.Contains(values[0], ",") {
			return controlclient.Principal{}, bearerAuthenticationError()
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return controlclient.Principal{}, bearerAuthenticationError()
		}
		provided := sha256.Sum256([]byte(parts[1]))
		if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			return controlclient.Principal{}, bearerAuthenticationError()
		}
		return principal, nil
	}), nil
}

func bearerAuthenticationError() error {
	return errorcode.New(errorcode.Unauthenticated, "controlserver: bearer authentication failed")
}
