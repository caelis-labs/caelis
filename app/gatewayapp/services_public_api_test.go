package gatewayapp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/ports/gateway"
)

func TestStackExposesNarrowKernelServices(t *testing.T) {
	t.Parallel()

	var stack *Stack
	if got := stack.KernelTurns(); got != nil {
		t.Fatalf("nil Stack.KernelTurns() = %#v, want nil", got)
	}
	if got := stack.KernelSessions(); got != nil {
		t.Fatalf("nil Stack.KernelSessions() = %#v, want nil", got)
	}
	if got := stack.KernelControlPlane(); got != nil {
		t.Fatalf("nil Stack.KernelControlPlane() = %#v, want nil", got)
	}
	if got := stack.KernelStreams(); got != nil {
		t.Fatalf("nil Stack.KernelStreams() = %#v, want nil", got)
	}
	requireKernelTurnService(stack.KernelTurns())
	requireKernelSessionService(stack.KernelSessions())
	requireKernelControlPlaneService(stack.KernelControlPlane())
	requireKernelStreamProvider(stack.KernelStreams())
	requireKernelService(stack.Kernel())
}

func requireKernelTurnService(gateway.TurnService)                 {}
func requireKernelSessionService(gateway.SessionService)           {}
func requireKernelControlPlaneService(gateway.ControlPlaneService) {}
func requireKernelStreamProvider(gateway.StreamProvider)           {}
func requireKernelService(gateway.Service)                         {}

func TestStackDoesNotExposeInternalGatewayImplementation(t *testing.T) {
	t.Parallel()

	stackType := reflect.TypeOf(Stack{})
	if field, ok := stackType.FieldByName("Gateway"); ok && field.IsExported() {
		t.Fatalf("Stack exports Gateway field of type %s; expose ports/gateway service accessors instead", field.Type)
	}

	narrowMethods := []struct {
		name string
		want reflect.Type
	}{
		{name: "KernelTurns", want: reflect.TypeOf((*gateway.TurnService)(nil)).Elem()},
		{name: "KernelSessions", want: reflect.TypeOf((*gateway.SessionService)(nil)).Elem()},
		{name: "KernelControlPlane", want: reflect.TypeOf((*gateway.ControlPlaneService)(nil)).Elem()},
		{name: "KernelStreams", want: reflect.TypeOf((*gateway.StreamProvider)(nil)).Elem()},
	}
	for _, tt := range narrowMethods {
		method, ok := reflect.TypeOf((*Stack)(nil)).MethodByName(tt.name)
		if !ok {
			t.Fatalf("Stack.%s() is not exported", tt.name)
		}
		if method.Type.NumOut() != 1 {
			t.Fatalf("%s() returns %d values, want 1", tt.name, method.Type.NumOut())
		}
		if got := method.Type.Out(0); got != tt.want {
			t.Fatalf("%s() returns %s, want %s", tt.name, got, tt.want)
		}
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
