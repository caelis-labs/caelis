package httpclient

import (
	"context"
	"net/http"
	"net/url"

	"github.com/caelis-labs/caelis/control/application"
)

func applicationBackgroundGrantPath(sessionID, grantID string) string {
	return applicationSessionPath(sessionID) + "/background-grants/" + url.PathEscape(grantID)
}

// CreateApplicationBackgroundGrant records an embedding's prior user
// authorization. Persist operation_id before dispatch; it is not a scheduler.
func (c *Client) CreateApplicationBackgroundGrant(ctx context.Context, sessionID string, req application.BackgroundGrantRequest) (application.BackgroundGrant, error) {
	return applicationJSON[application.BackgroundGrant](ctx, c, http.MethodPost, applicationSessionPath(sessionID)+"/background-grants", nil, req, req.OperationID, false)
}

// ListApplicationBackgroundGrants includes revoked grants for recovery.
func (c *Client) ListApplicationBackgroundGrants(ctx context.Context, sessionID string) ([]application.BackgroundGrant, error) {
	return doFocusedJSON[[]application.BackgroundGrant](ctx, c, http.MethodGet, applicationSessionPath(sessionID)+"/background-grants", nil)
}

// ApplicationBackgroundGrant returns one grant under its exact Session.
func (c *Client) ApplicationBackgroundGrant(ctx context.Context, sessionID, grantID string) (application.BackgroundGrant, error) {
	return doFocusedJSON[application.BackgroundGrant](ctx, c, http.MethodGet, applicationBackgroundGrantPath(sessionID, grantID), nil)
}

// RevokeApplicationBackgroundGrant stops new admissions, not work already accepted.
func (c *Client) RevokeApplicationBackgroundGrant(ctx context.Context, sessionID, grantID string) (application.BackgroundGrant, error) {
	return doFocusedJSON[application.BackgroundGrant](ctx, c, http.MethodPost, applicationBackgroundGrantPath(sessionID, grantID)+"/revoke", struct{}{})
}
