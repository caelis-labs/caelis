package gatewayapp

import (
	"context"
	"path/filepath"

	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	acptaskstream "github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

// hostControlAssembly holds the focused late-binding points produced while
// constructing Host Control services. It is consumed exactly once by Runtime
// activation and is not retained as another process authority.
type hostControlAssembly struct {
	runtimeStateReader *controlRuntimeStateReader
	participantHandles *participantHandleReader
	taskStreamRouter   *hostTaskStreamService
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
	controlOperations, err := appserver.NewFileOperationStoreWithConfig(
		filepath.Join(storeDir, "control-operations"),
		appserver.OperationRetentionConfig{TerminalRetention: cfg.ControlOperationRetention},
	)
	if err != nil {
		return hostControlAssembly{}, err
	}
	acpPreparations, err := newACPPreparationStore(storeDir)
	if err != nil {
		return hostControlAssembly{}, err
	}
	if err := controlOperations.Initialize(context.Background()); err != nil {
		return hostControlAssembly{}, err
	}
	effectiveOperationRetention, err := controlOperations.EffectiveTerminalRetention(context.Background())
	if err != nil {
		return hostControlAssembly{}, err
	}
	stack.controlOperationRetention = effectiveOperationRetention
	stack.operations = controlOperations
	stack.commandBackend.acpPreparations = acpPreparations

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
	controlTaskStreams, err := controltaskstream.New(controltaskstream.Config{
		Tasks:           stack.composition.authorities.taskStore,
		Streams:         func() stream.Service { return taskStreamRouter },
		Sessions:        stack.composition.sessions,
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
	}, nil
}

func activateHostRuntime(stack *Stack, assembly hostControlAssembly) error {
	stack.composition.authorities.lifecycleCtx, stack.lifecycleCancel = context.WithCancel(context.Background())
	if err := stack.composition.buildInitialGatewayRuntime(context.Background()); err != nil {
		stack.lifecycleCancel()
		return err
	}
	mailboxRouter := &hostedChildMailboxRouter{}
	stack.composition.authorities.hostedChildMailbox = mailboxRouter.deliver
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
	if err := mailboxRouter.bind(sessionRuntimes); err != nil {
		_ = stack.Close()
		return err
	}
	return nil
}
