package acpsurface

import (
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	surfaceacp "github.com/caelis-labs/caelis/surfaces/acp"
)

func newTestAgentFromStack(stack *gatewayapp.Stack) (*surfaceacp.ProductAgent, error) {
	appServer, err := local.NewAppServer(stack)
	if err != nil {
		return nil, err
	}
	clients, err := appServer.Bind(appserver.Principal{ID: stack.UserID()})
	if err != nil {
		return nil, err
	}
	systemSessionClient, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{
		ID: stack.UserID(), Roles: []string{appserver.RoleSystemSessionRuntime},
	})
	if err != nil {
		return nil, err
	}
	return surfaceacp.NewFromClients(surfaceacp.ClientsConfig{
		Clients:             clients,
		AppName:             stack.AppName(),
		UserID:              stack.UserID(),
		WorkspaceKey:        strings.TrimSpace(stack.Workspace().Key),
		WorkspaceCWD:        strings.TrimSpace(stack.Workspace().CWD),
		SystemSessionClient: systemSessionClient,
	})
}
