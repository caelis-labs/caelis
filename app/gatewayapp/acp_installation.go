package gatewayapp

import (
	"context"
	"path/filepath"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	"github.com/caelis-labs/caelis/control/agents"
)

func installACPConnection(ctx context.Context, request agents.ACPPrepareRequest) (agents.Connection, bool, error) {
	agent, ok := agentregistry.LookupConnectableAgent(request.AdapterID)
	if !ok || agent.Installation == nil || request.Launcher != agents.LauncherChoiceInstalled {
		return agents.Connection{}, false, errorcode.New(errorcode.InvalidArgument, "This agent requires manual setup.")
	}
	installed, err := agent.Installation.Install(ctx, *request.Install)
	if err != nil {
		return agents.Connection{}, installed, errorcode.New(errorcode.InvalidArgument, err.Error())
	}
	preset, _ := agentregistry.LookupInstalledAgent(request.AdapterID)
	connection := agents.Connection{ID: request.AdapterID, Name: request.AdapterID, Launcher: agents.Launcher{
		Kind: agents.LaunchKindExecutable, Command: filepath.Join(request.Install.Directory, preset.Command), Args: preset.Args, Env: preset.Env,
	}}
	return connection, true, agents.ValidateConnection(connection)
}
