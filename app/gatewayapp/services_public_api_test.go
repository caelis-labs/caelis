package gatewayapp

import (
	"reflect"
	"slices"
	"testing"
)

func TestStackPublicMethodsStayAtDeclaredHostBoundary(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"ACPPreparationReads": true, "ACPSurface": true, "AgentBindings": true,
		"AgentCommands": true, "AgentMessageDelivery": true, "Agents": true,
		"AppName": true, "CanRecoverControlCommand": true, "Close": true,
		"CompactSession":        true,
		"ConfigurationCommands": true, "ConfigurationRevision": true,
		"ControlClient": true, "ControlClientRuntimeState": true,
		"ControlParticipants": true, "ControlRuntimeView": true,
		"ControlRuntimes": true, "ControlStatus": true,
		"ControlTerminalStreams": true, "ExecuteControlCommand": true,
		"LoadHistory": true, "Models": true,
		"ParticipantHandles": true, "PluginCommands": true, "Plugins": true,
		"PreflightSandbox": true, "PresentationDependencies": true,
		"Quiesce": true, "RecoverControlCommand": true,
		"ResolveHandlePlacement": true, "Sessions": true,
		"SetBuiltInChildControl": true, "Skills": true,
		"StartApprovalRecovery": true, "StartSubagent": true,
		"StartSubagentWithOptions": true, "Status": true, "TaskStreams": true,
		"UserID": true, "WaitApprovalRecovery": true, "WaitSubagentTask": true,
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
