package gatewayapp

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/kernel"
)

func TestStackExposesPublicKernelService(t *testing.T) {
	t.Parallel()

	var stack *Stack
	svc := stack.Kernel()
	if svc != nil {
		t.Fatalf("nil Stack.Kernel() = %#v, want nil", svc)
	}
	requireKernelService(svc)
}

func requireKernelService(kernel.Service) {}
