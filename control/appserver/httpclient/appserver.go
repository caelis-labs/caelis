package httpclient

import (
	"errors"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

// AppServerClients returns the complete focused AppServer client set backed by
// one authenticated HTTP Control client. The same transport-neutral contracts
// are used by embedded AppServer bindings.
func AppServerClients(client *Client) (appserver.AppServerClients, error) {
	if client == nil {
		return appserver.AppServerClients{}, errors.New("control http client: client is required")
	}
	clients := appserver.AppServerClients{
		Sessions:      client,
		Participants:  client,
		AgentMessages: client,
		Status:        client,
		Configuration: client,
		Agents:        client,
		Completion:    client,
		Plugins:       client,
		Presentation:  client,
		Terminal:      client,
	}
	if err := clients.Validate(); err != nil {
		return appserver.AppServerClients{}, err
	}
	return clients, nil
}

// BindAppServer binds one authenticated HTTP Control client into the complete
// product AppServer capability set plus the independent Task observation side
// channel. Surfaces consume these clients only; Runtime and Kernel handles stay
// inside the Host process.
func BindAppServer(client *Client) (appserver.AppServerClients, taskstream.Client, error) {
	clients, err := AppServerClients(client)
	if err != nil {
		return appserver.AppServerClients{}, nil, err
	}
	tasks, err := NewTaskClient(client)
	if err != nil {
		return appserver.AppServerClients{}, nil, err
	}
	return clients, tasks, nil
}

var (
	_ appserver.SessionClient       = (*Client)(nil)
	_ appserver.ParticipantClient   = (*Client)(nil)
	_ appserver.AgentMessageClient  = (*Client)(nil)
	_ appserver.StatusClient        = (*Client)(nil)
	_ appserver.ConfigurationClient = (*Client)(nil)
	_ appserver.AgentClient         = (*Client)(nil)
	_ appserver.CompletionClient    = (*Client)(nil)
	_ appserver.PluginClient        = (*Client)(nil)
	_ appserver.PresentationClient  = (*Client)(nil)
	_ appserver.TerminalClient      = (*Client)(nil)
	_ taskstream.Client             = (*TaskClient)(nil)
)
