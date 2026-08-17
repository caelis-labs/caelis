package local

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
)

func TestAppServerLeafServicesDoNotRetainConcreteHost(t *testing.T) {
	t.Parallel()

	assertAppServerLeafServicesDoNotRetain(t, "gatewayapp.Stack", reflect.TypeFor[gatewayapp.Stack]())
}

func TestAppServerLeafServicesDoNotRetainRuntimeLease(t *testing.T) {
	t.Parallel()

	assertAppServerLeafServicesDoNotRetain(t, "gatewayapp.ControlRuntimeLease", reflect.TypeFor[gatewayapp.ControlRuntimeLease]())
}

func assertAppServerLeafServicesDoNotRetain(t *testing.T, targetName string, targetType reflect.Type) {
	t.Helper()
	for _, test := range []struct {
		name string
		typ  reflect.Type
	}{
		{name: "Agent message", typ: reflect.TypeFor[AgentMessageService]()},
		{name: "Status", typ: reflect.TypeFor[StatusService]()},
		{name: "Configuration", typ: reflect.TypeFor[ConfigurationService]()},
		{name: "Agent", typ: reflect.TypeFor[AgentService]()},
		{name: "Completion", typ: reflect.TypeFor[CompletionService]()},
		{name: "Plugin", typ: reflect.TypeFor[PluginService]()},
		{name: "Presentation", typ: reflect.TypeFor[PresentationService]()},
		{name: "Terminal", typ: reflect.TypeFor[TerminalService]()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if path, ok := retainedConcreteType(test.typ, targetType, nil); ok {
				t.Fatalf("%s service retains %s through %s", test.name, targetName, path)
			}
		})
	}
}

func retainedConcreteType(typ reflect.Type, targetType reflect.Type, seen map[reflect.Type]bool) (string, bool) {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Array || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Chan {
		typ = typ.Elem()
	}
	if typ == targetType {
		return typ.String(), true
	}
	if typ.Kind() == reflect.Map {
		if path, ok := retainedConcreteType(typ.Key(), targetType, seen); ok {
			return "map key." + path, true
		}
		return retainedConcreteType(typ.Elem(), targetType, seen)
	}
	if typ.Kind() != reflect.Struct || typ.PkgPath() != reflect.TypeFor[AgentService]().PkgPath() {
		return "", false
	}
	if seen == nil {
		seen = map[reflect.Type]bool{}
	}
	if seen[typ] {
		return "", false
	}
	seen[typ] = true
	for index := range typ.NumField() {
		field := typ.Field(index)
		if path, ok := retainedConcreteType(field.Type, targetType, seen); ok {
			return field.Name + "." + path, true
		}
	}
	return "", false
}
