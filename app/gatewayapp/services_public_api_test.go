package gatewayapp

import (
	"reflect"
	"slices"
	"testing"
)

func TestStackPublicMethodsStayAtDeclaredHostBoundary(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"ACPPreparationReads": true, "AgentBindings": true,
		"AgentCommands": true, "Agents": true,
		"AppName": true, "Close": true,
		"ConfigurationCommands": true,
		"ControlClient":         true,
		"ControlKernelReads":    true,
		"ControlPluginReads":    true, "ControlParticipants": true,
		"ControlRuntimes": true, "ControlStatus": true,
		"ControlTerminalStreams": true,
		"Models":                 true,
		"PluginCommands":         true, "PresentationDependencies": true, "PresentationSource": true,
		"Quiesce":                true,
		"Sessions":               true,
		"SetBuiltInChildControl": true, "Skills": true,
		"StartApprovalRecovery": true, "TaskStreams": true,
		"UserID": true, "WaitApprovalRecovery": true,
		"Workspace": true, "WorkspaceReads": true,
	}

	stackType := reflect.TypeFor[*Stack]()
	var unexpected []string
	for index := range stackType.NumMethod() {
		name := stackType.Method(index).Name
		if want[name] {
			delete(want, name)
			continue
		}
		unexpected = append(unexpected, name)
	}
	var missing []string
	for name := range want {
		missing = append(missing, name)
	}
	slices.Sort(unexpected)
	slices.Sort(missing)
	if len(unexpected) > 0 || len(missing) > 0 {
		t.Fatalf("Stack public methods changed: unexpected=%v missing=%v; expose focused services or update this boundary deliberately", unexpected, missing)
	}
}

func TestPluginReadServiceExposesOnlyReadMethods(t *testing.T) {
	t.Parallel()

	want := []string{"Inspect", "List", "ListMarketplaces"}
	typ := reflect.TypeFor[PluginReadService]()
	got := make([]string, 0, typ.NumMethod())
	for index := range typ.NumMethod() {
		got = append(got, typ.Method(index).Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("PluginReadService methods = %v, want read-only %v", got, want)
	}
}

func TestProductionRuntimeCompositionPreservesStreamsThroughDecorators(t *testing.T) {
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	defer stack.Close()
	provider := stack.runtimeProjection().KernelStreams()
	if provider == nil || provider.Streams() == nil {
		t.Fatalf("KernelStreams().Streams() = %#v, want production task streams", provider)
	}
}
