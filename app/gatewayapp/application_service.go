package gatewayapp

import appserver "github.com/caelis-labs/caelis/control/appserver"

// Applications exposes the focused Control application service to Host-private
// adapters, never a Session Runtime lease or second executor.
func (s *Stack) Applications() *appserver.ApplicationService {
	if s == nil {
		return nil
	}
	return s.applications
}
