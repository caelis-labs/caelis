package cli

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/app/controlserver"
	appserver "github.com/caelis-labs/caelis/control/appserver"
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

func controlAuthenticatorWithACPIngress(
	ordinaryToken string,
	ordinaryPrincipal appserver.Principal,
	acpIngressToken string,
) (controlserver.Authenticator, error) {
	ordinaryAuthenticator, err := controlserver.BearerTokenAuthenticator(ordinaryToken, ordinaryPrincipal)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(ordinaryToken) == strings.TrimSpace(acpIngressToken) {
		return nil, errors.New("cli: ordinary Control and ACP ingress bearer credentials must be distinct")
	}
	acpIngressPrincipal := ordinaryPrincipal
	acpIngressPrincipal.Roles = append(
		append([]string(nil), acpIngressPrincipal.Roles...),
		appserver.RoleACPIngress,
	)
	acpIngressAuthenticator, err := controlserver.BearerTokenAuthenticator(acpIngressToken, acpIngressPrincipal)
	if err != nil {
		return nil, err
	}
	return anyControlAuthenticator(ordinaryAuthenticator, acpIngressAuthenticator), nil
}

func anyControlAuthenticator(authenticators ...controlserver.Authenticator) controlserver.Authenticator {
	return controlserver.AuthenticatorFunc(func(request *http.Request) (appserver.Principal, error) {
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
		return appserver.Principal{}, authenticationErr
	})
}
