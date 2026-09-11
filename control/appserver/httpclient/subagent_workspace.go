package httpclient

import (
	"context"
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

func (c *Client) SubmitSubagentInput(ctx context.Context, req appserver.SubagentInputRequest) (collaboration.UserInputStatus, error) {
	return doFocusedSessionJSON[collaboration.UserInputStatus](ctx, c, req.SessionID, "/subagents/input", req)
}
func (c *Client) SubagentInputStatuses(ctx context.Context, req appserver.SubagentInputStatusRequest) ([]collaboration.UserInputStatus, error) {
	return doFocusedSessionJSON[[]collaboration.UserInputStatus](ctx, c, req.SessionID, "/subagents/input-status", req)
}
func (c *Client) LoadUIPreferences(ctx context.Context) (uipreferences.Preferences, error) {
	return doFocusedJSON[uipreferences.Preferences](ctx, c, http.MethodGet, "/presentation/preferences", nil)
}
func (c *Client) SaveUIPreferences(ctx context.Context, p uipreferences.Preferences) error {
	_, err := doFocusedJSON[struct{}](ctx, c, http.MethodPut, "/presentation/preferences", p)
	return err
}
