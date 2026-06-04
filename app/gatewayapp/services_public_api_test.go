package gatewayapp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
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

func requireKernelService(gateway.Service) {}

func TestStackDoesNotExposeInternalGatewayImplementation(t *testing.T) {
	t.Parallel()

	stackType := reflect.TypeOf(Stack{})
	if field, ok := stackType.FieldByName("Gateway"); ok && field.IsExported() {
		t.Fatalf("Stack exports Gateway field of type %s; expose ports/gateway service accessors instead", field.Type)
	}

	method, ok := reflect.TypeOf((*Stack)(nil)).MethodByName("CurrentGateway")
	if !ok {
		return
	}
	if method.Type.NumOut() != 1 {
		t.Fatalf("CurrentGateway() returns %d values, want 1", method.Type.NumOut())
	}
	if got := method.Type.Out(0).String(); strings.Contains(got, "/internal/kernel") {
		t.Fatalf("CurrentGateway() returns internal implementation type %s", got)
	}
}
