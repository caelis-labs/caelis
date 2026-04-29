package gateway

import "testing"

func TestGatewaySatisfiesCoreServiceSlices(t *testing.T) {
	t.Parallel()

	var _ SessionService = (*Gateway)(nil)
	var _ TurnService = (*Gateway)(nil)
	var _ ControlPlaneService = (*Gateway)(nil)
	var _ CoreService = (*Gateway)(nil)
}

func TestHostSatisfiesHostService(t *testing.T) {
	t.Parallel()

	var _ HostService = (*Host)(nil)
}
