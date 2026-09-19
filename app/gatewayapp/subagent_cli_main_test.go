package gatewayapp_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/cli"
)

// The built-in child launches the current executable with "acp". Give that
// child the real CLI entry point while ordinary helper processes still run
// their selected Go tests. This exercises the production stdio/Host boundary.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "acp" || os.Args[1] == "collaboration") {
		if err := cli.Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestHostedSubagentUserInputThroughACPAndContextReplay(t *testing.T) {
	gatewayapp.RunHostedSubagentUserInputTest(t, func(host *gatewayapp.Stack) (appserver.AppServerServices, error) {
		server, err := local.NewAppServer(host)
		if err != nil {
			return appserver.AppServerServices{}, err
		}
		return server.Services, nil
	})
}

func TestLiveCodexControllerCollaboration(t *testing.T) {
	gatewayapp.RunLiveControllerCollaborationTest(t, func(host *gatewayapp.Stack) (appserver.AppServerServices, error) {
		server, err := local.NewAppServer(host)
		if err != nil {
			return appserver.AppServerServices{}, err
		}
		return server.Services, nil
	})
}
