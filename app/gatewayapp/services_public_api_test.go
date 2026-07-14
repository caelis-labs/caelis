package gatewayapp

import "testing"

func TestProductionStackPreservesRuntimeStreamsThroughDecorators(t *testing.T) {
	t.Parallel()

	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	defer stack.Close()
	provider := stack.KernelStreams()
	if provider == nil || provider.Streams() == nil {
		t.Fatalf("KernelStreams().Streams() = %#v, want production task streams", provider)
	}
}
