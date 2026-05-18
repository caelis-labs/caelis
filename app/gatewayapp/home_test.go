package gatewayapp

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/internal/testenv"
)

func setHomeForGatewayAppTest(t *testing.T, home string) {
	t.Helper()
	testenv.SetHome(t, home)
}
