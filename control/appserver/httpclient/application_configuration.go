package httpclient

import (
	"context"
	"net/http"
	"net/url"

	"github.com/caelis-labs/caelis/control/application"
)

// ApplicationConfiguration reads the desired profile and the latest admitted
// request revision, independent of canonical Session history revisions.
func (c *Client) ApplicationConfiguration(ctx context.Context, sessionID string) (application.Configuration, error) {
	return doFocusedJSON[application.Configuration](ctx, c, http.MethodGet, applicationSessionPath(sessionID)+"/configuration", nil)
}

// UpdateApplicationConfiguration commits a configuration CAS without submitting
// user input. Preserve OperationID across uncertain responses; retries return the
// original receipt and never execute the model.
func (c *Client) UpdateApplicationConfiguration(ctx context.Context, sessionID string, req application.UpdateConfigurationRequest) (application.Configuration, error) {
	return applicationJSON[application.Configuration](ctx, c, http.MethodPost, applicationSessionPath(sessionID)+"/configuration", nil, req, req.OperationID, false)
}

// ApplicationConfigurationOperation reconciles one update after reconnect or
// restart without repeating input or inferring execution from a saved profile.
func (c *Client) ApplicationConfigurationOperation(ctx context.Context, operationID string) (application.Configuration, error) {
	return doFocusedJSON[application.Configuration](ctx, c, http.MethodGet, "/application/configuration-operations/"+url.PathEscape(operationID), nil)
}
