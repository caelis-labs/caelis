package httpclient

import (
	"context"
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
)

// RecordInteraction records a command conversation message through the native
// Session mutation API, preserving its operation receipt and scoped credential.
func (c *Client) RecordInteraction(ctx context.Context, request appserver.RecordInteractionRequest) (appserver.CommandResult, error) {
	sessionID, err := remotePathID("session", request.SessionID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	return c.doCommand(ctx, http.MethodPost, "/sessions/"+sessionID+"/interactions", request.WriteBase, request)
}
