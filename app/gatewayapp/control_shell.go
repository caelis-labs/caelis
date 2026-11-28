package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/shellproc"
)

// ControlShell returns the Session-free Shell service on trusted local Hosts.
func (s *Stack) ControlShell() *appserver.ShellService { return s.controlShell }

// EnableControlShell registers execution without starting any worker or probing
// its sandbox. The serving transport separately attests to credential locality.
func (s *Stack) EnableControlShell() bool {
	if !shellproc.Supported() {
		return false
	}
	if s.controlShell == nil {
		store := s.composition.authorities.storeDir
		helper := s.composition.sandbox.HelperPath
		authority := s.composition.authorities.sandboxHostAuthorityDir
		s.controlShell = appserver.NewShellService(s.UserID(), func(ctx context.Context, req appserver.ShellRequest) (appserver.ShellResult, error) {
			return shellproc.Run(ctx, req, store, helper, authority)
		})
	}
	return true
}
