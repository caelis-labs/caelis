package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	acptaskstream "github.com/caelis-labs/caelis/control/appserver/taskstream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

// hostControlAssembly holds the focused late-binding points produced while
// constructing Host Control services. It is consumed exactly once by Runtime
// activation and is not retained as another process authority.
type hostControlAssembly struct {
	runtimeStateReader *controlRuntimeStateReader
	participantHandles *participantHandleReader
	taskStreamRouter   *hostTaskStreamService
	taskDirectory      *controltaskstream.DirectoryIndex
}

func assembleHostControlServices(stack *Stack, cfg Config, storeDir string, cursorSecret []byte) (hostControlAssembly, error) {
	runtimeStateReader, err := newControlRuntimeStateReader(&stack.composition)
	if err != nil {
		return hostControlAssembly{}, err
	}
	participantHandles, err := newParticipantHandleReader(&stack.composition)
	if err != nil {
		return hostControlAssembly{}, err
	}
	controlState, err := appserver.NewStateService(appserver.StateServiceConfig{
		Sessions: stack.composition.sessions, Runtime: runtimeStateReader, Feeds: stack.composition.authorities.controlFeeds,
		PrepareReconnect:  stack.commandBackend.prepareControlClientReconnect,
		RetainObservation: stack.commandBackend.retainControlClientObservation,
	})
	if err != nil {
		return hostControlAssembly{}, err
	}
	controlOperations, err := appserver.NewSQLiteOperationStoreWithConfig(
		controlStoreDatabasePath(storeDir),
		appserver.OperationRetentionConfig{TerminalRetention: cfg.ControlOperationRetention},
	)
	if err != nil {
		return hostControlAssembly{}, err
	}
	acpPreparations, err := newACPPreparationStore(storeDir)
	if err != nil {
		_ = controlOperations.Close()
		return hostControlAssembly{}, err
	}
	currentConfig, err := stack.composition.authorities.store.LoadContext(context.Background())
	if err != nil {
		_ = acpPreparations.Close()
		_ = controlOperations.Close()
		return hostControlAssembly{}, err
	}
	if err := reclaimRetiredACPAgentStore(context.Background(), storeDir, currentConfig, acpPreparations); err != nil {
		_ = acpPreparations.Close()
		_ = controlOperations.Close()
		return hostControlAssembly{}, err
	}
	stack.operations = controlOperations
	stack.commandBackend.acpPreparations = acpPreparations
	if err := controlOperations.Initialize(context.Background()); err != nil {
		return hostControlAssembly{}, err
	}
	effectiveOperationRetention, err := controlOperations.EffectiveTerminalRetention(context.Background())
	if err != nil {
		return hostControlAssembly{}, err
	}
	stack.controlOperationRetention = effectiveOperationRetention

	sessionAuthorizer := appserver.SessionAuthorizer{Sessions: stack.composition.sessions}
	controlCommands, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{Sessions: sessionAuthorizer},
		Operations: controlOperations,
		Backend:    stack.commandBackend,
	})
	if err != nil {
		return hostControlAssembly{}, err
	}
	controlClient, err := appserver.NewClient(appserver.ClientConfig{
		Commands: controlCommands, State: controlState, Feeds: stack.composition.authorities.controlFeeds,
		Authorizer:         sessionAuthorizer,
		ParticipantHandles: participantHandles,
		Sessions:           stack.composition.sessions,
	})
	if err != nil {
		return hostControlAssembly{}, err
	}
	stack.controlClient = controlClient
	stack.configurationCommands = controlCommands
	stack.agentCommands = controlCommands
	stack.pluginCommands = controlCommands

	taskStreamRouter := &hostTaskStreamService{host: &stack.composition}
	taskDirectory := controltaskstream.NewDirectoryIndex()
	stack.composition.taskCommitted = taskDirectory.Notify
	controlTaskStreams, err := controltaskstream.New(controltaskstream.Config{
		Tasks:           stack.composition.authorities.taskStore,
		Streams:         func() stream.Service { return taskStreamRouter },
		Sessions:        stack.composition.sessions,
		Directory:       taskDirectory,
		SubagentHistory: subagentHistoryService{composition: &stack.composition},
		Authorizer:      taskStreamAuthorizer{inner: sessionAuthorizer},
		Secret:          cursorSecret,
	})
	if err != nil {
		return hostControlAssembly{}, err
	}
	stack.taskStreams = acptaskstream.New(controlTaskStreams)
	return hostControlAssembly{
		runtimeStateReader: runtimeStateReader,
		participantHandles: participantHandles,
		taskStreamRouter:   taskStreamRouter,
		taskDirectory:      taskDirectory,
	}, nil
}

func activateHostRuntime(stack *Stack, assembly hostControlAssembly) error {
	stack.composition.authorities.lifecycleCtx, stack.lifecycleCancel = context.WithCancel(context.Background())
	if err := stack.composition.buildInitialGatewayRuntime(context.Background()); err != nil {
		stack.lifecycleCancel()
		return err
	}
	inputRouter := &hostedChildInputRouter{}
	stack.composition.authorities.hostedChildInput = inputRouter.route
	assemblyDeps, err := newSessionRuntimeAssemblyDeps(stack)
	if err != nil {
		_ = stack.Close()
		return err
	}
	runtimeAssembler, err := newWorkspaceConfigAssembler(assemblyDeps)
	if err != nil {
		_ = stack.Close()
		return err
	}
	sessionRuntimes, err := newSessionRuntimeRegistry(sessionRuntimeRegistryConfig{
		Sessions:         stack.composition.sessions,
		Tasks:            stack.composition.authorities.taskStore,
		LifecycleContext: stack.composition.authorities.lifecycleCtx,
		DefaultWorkspace: stack.composition.workspace,
		ModelRecovery:    stack.modelRecovery,
		Assembler:        runtimeAssembler,
		TaskCommitted:    assembly.taskDirectory.Notify,
	})
	if err != nil {
		_ = stack.Close()
		return err
	}
	stack.sessionRuntimes = sessionRuntimes
	for _, bind := range []func(*sessionRuntimeRegistry) error{
		stack.commandBackend.bindSessionRuntimes,
		assembly.runtimeStateReader.bindRegistry,
		assembly.participantHandles.bindRegistry,
	} {
		if err := bind(sessionRuntimes); err != nil {
			_ = stack.Close()
			return err
		}
	}
	assembly.taskStreamRouter.registry = sessionRuntimes
	if err := inputRouter.bind(sessionRuntimes); err != nil {
		_ = stack.Close()
		return err
	}
	return nil
}
