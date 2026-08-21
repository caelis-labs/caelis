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
	tasks, err := NewTaskClient(client)
	if err != nil {
		return appserver.AppServerClients{}, err
	}
	clients := appserver.AppServerClients{
		Sessions:      client,
		Participants:  client,
		Status:        client,
		Configuration: client,
		Agents:        client,
		Completion:    client,
		Plugins:       client,
		Presentation:  client,
		Terminal:      client,
		Tasks:         tasks,
	}
	if err := clients.Validate(); err != nil {
		return appserver.AppServerClients{}, err
	}
	return clients, nil
}

var (
	_ appserver.SessionClient       = (*Client)(nil)
	_ appserver.ParticipantClient   = (*Client)(nil)
	_ appserver.StatusClient        = (*Client)(nil)
	_ appserver.ConfigurationClient = (*Client)(nil)
	_ appserver.AgentClient         = (*Client)(nil)
	_ appserver.CompletionClient    = (*Client)(nil)
	_ appserver.PluginClient        = (*Client)(nil)
	_ appserver.PresentationClient  = (*Client)(nil)
	_ appserver.TerminalClient      = (*Client)(nil)
	_ taskstream.Client             = (*TaskClient)(nil)
	_ taskstream.DirectoryClient    = (*TaskClient)(nil)
)
