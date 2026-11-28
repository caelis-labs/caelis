package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/shellproc"
)

func newControlShell(principal, store, helper, authority string) *appserver.ShellService {
	if !shellproc.Supported() {
		return nil
	}
	return appserver.NewShellService(principal, func(ctx context.Context, req appserver.ShellRequest) (appserver.ShellResult, error) {
		return shellproc.Run(ctx, req, store, helper, authority)
	})
}
