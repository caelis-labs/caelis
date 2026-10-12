package cli

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

type receiptPluginClient struct {
	appserver.PluginClient
	outcome appserver.Outcome
}

func (p receiptPluginClient) RemovePlugin(ctx context.Context, req appserver.RemovePluginRequest) (appserver.CommandResult, error) {
	if p.outcome == appserver.OutcomeCommitted {
		result, err := p.PluginClient.RemovePlugin(ctx, req)
		result.Detail = "cleanup warning"
		return result, err
	}
	return appserver.CommandResult{OperationID: req.OperationID, Outcome: p.outcome}, nil
}

func TestCLIPluginRemoveReceiptMatchesRegistry(t *testing.T) {
	for _, outcome := range []appserver.Outcome{appserver.OutcomeRejected, appserver.OutcomeConflicted, appserver.OutcomeUnknown, appserver.OutcomeCommitted} {
		t.Run(string(outcome), func(t *testing.T) {
			resetCLIPluginEnv(t)
			t.Chdir(t.TempDir())
			store, root := cliTestStoreDir(t), t.TempDir()
			writeCLIPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo"}`)
			actual := roundTripProductClientOpener(t)
			if _, err := runCLIPluginJSON(t, t.Context(), actual, "install", root, "--json", "--embedded", "--store-dir", store); err != nil {
				t.Fatal(err)
			}
			injected := func(ctx context.Context, cfg gatewayapp.Config, options productClientOptions) (*productClients, error) {
				clients, err := actual(ctx, cfg, options)
				if err == nil {
					clients.Clients.Plugins = receiptPluginClient{PluginClient: clients.Clients.Plugins, outcome: outcome}
				}
				return clients, err
			}
			result, err := runCLIPluginJSON(t, t.Context(), injected, "remove", "demo", "--json", "--embedded", "--store-dir", store)
			if err == nil || result.Receipt == nil || result.Receipt.Outcome != outcome {
				t.Fatalf("missing failure receipt: %+v, %v", result, err)
			}
			registered, statusErr := runCLIPluginJSON(t, t.Context(), actual, "status", "demo", "--json", "--embedded", "--store-dir", store)
			if statusErr != nil || result.Installed != registered.Installed || (!result.Installed && result.Plugin != nil) {
				t.Fatalf("remove receipt contradicts registry: %+v, %+v, %v", result, registered, statusErr)
			}
		})
	}
}
