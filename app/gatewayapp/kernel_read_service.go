package gatewayapp

// KernelReadService is the focused Host-private live execution projection used
// by AppServer assemblers. It exposes no Runtime assembly or lifecycle control.
type KernelReadService struct {
	composition *runtimeComposition
}

// ControlKernelReads returns the focused Kernel read service for the process
// default Runtime. Session-specific adapters obtain the same capability from
// an authorized ControlRuntimeLease.
func (s *Stack) ControlKernelReads() KernelReadService {
	if s == nil {
		return KernelReadService{}
	}
	return KernelReadService{composition: &s.composition}
}

// TurnState returns the read-only live Turn projection.
func (s KernelReadService) TurnState() KernelTurnReader {
	if s.composition == nil {
		return nil
	}
	return s.composition.KernelTurnState()
}

// ControlPlaneState returns the read-only controller and participant
// projection.
func (s KernelReadService) ControlPlaneState() KernelControlPlaneReader {
	if s.composition == nil {
		return nil
	}
	return s.composition.KernelControlPlaneState()
}
