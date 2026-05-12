package kernel

import "testing"

func TestGatewaySatisfiesCoreServiceSlices(t *testing.T) {
	t.Parallel()

	var _ SessionService = (*Gateway)(nil)
	var _ TurnService = (*Gateway)(nil)
	var _ ControlPlaneService = (*Gateway)(nil)
	var _ Service = (*Gateway)(nil)
}
