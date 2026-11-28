package httpclient

import (
	"context"
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
)

// ExecuteShell invokes the Shell API after the caller checks CapabilityShellExecution.
// A lost response leaves any effects already performed without a durable receipt.
func (c *Client) ExecuteShell(ctx context.Context, req appserver.ShellRequest) (appserver.ShellResult, error) {
	return doFocusedJSON[appserver.ShellResult](ctx, c, http.MethodPost, "/shell/execute", req)
}

var _ appserver.ShellClient = (*Client)(nil)
