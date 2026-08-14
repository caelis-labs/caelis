package cli

import (
	"errors"
	"net/http"
	"os"
	"sync"

	"github.com/caelis-labs/caelis/app/controlserver"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

func newEphemeralChildControlCredential() (string, string, func() error, error) {
	dir, err := os.MkdirTemp("", "caelis-control-child-credential-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := sync.OnceValue(func() error { return os.RemoveAll(dir) })
	path := controlserver.DefaultTokenFile(dir)
	token, err := controlserver.LoadOrCreateBearerToken(path)
	if err != nil {
		return "", "", nil, errors.Join(err, cleanup())
	}
	return path, token, cleanup, nil
}

func anyControlAuthenticator(authenticators ...controlserver.Authenticator) controlserver.Authenticator {
	return controlserver.AuthenticatorFunc(func(request *http.Request) (controlclient.Principal, error) {
		var authenticationErr error
		for _, authenticator := range authenticators {
			if authenticator == nil {
				continue
			}
			principal, err := authenticator.Authenticate(request)
			if err == nil {
				return principal, nil
			}
			authenticationErr = err
		}
		return controlclient.Principal{}, authenticationErr
	})
}
