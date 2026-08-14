package httpclient

import (
	"errors"

	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

// AppServerClients returns the complete focused AppServer client set backed by
// one authenticated HTTP Control client. The same transport-neutral contracts
// are used by embedded AppServer bindings.
func AppServerClients(client *Client) (controlclient.AppServerClients, error) {
	if client == nil {
		return controlclient.AppServerClients{}, errors.New("control http client: client is required")
	}
	clients := controlclient.AppServerClients{
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
		return controlclient.AppServerClients{}, err
	}
	return clients, nil
}

// BindAppServer binds one authenticated HTTP Control client into the complete
// product AppServer capability set plus the independent Task observation side
// channel. Surfaces consume these clients only; Runtime and Kernel handles stay
// inside the Host process.
func BindAppServer(client *Client) (controlclient.AppServerClients, taskstream.Client, error) {
	clients, err := AppServerClients(client)
	if err != nil {
		return controlclient.AppServerClients{}, nil, err
	}
	tasks, err := NewTaskClient(client)
	if err != nil {
		return controlclient.AppServerClients{}, nil, err
	}
	return clients, tasks, nil
}

var (
	_ controlclient.SessionClient       = (*Client)(nil)
	_ controlclient.ParticipantClient   = (*Client)(nil)
	_ controlclient.AgentMessageClient  = (*Client)(nil)
	_ controlclient.StatusClient        = (*Client)(nil)
	_ controlclient.ConfigurationClient = (*Client)(nil)
	_ controlclient.AgentClient         = (*Client)(nil)
	_ controlclient.CompletionClient    = (*Client)(nil)
	_ controlclient.PluginClient        = (*Client)(nil)
	_ controlclient.PresentationClient  = (*Client)(nil)
	_ controlclient.TerminalClient      = (*Client)(nil)
	_ taskstream.Client                 = (*TaskClient)(nil)
)
