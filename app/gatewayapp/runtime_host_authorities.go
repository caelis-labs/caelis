package gatewayapp

import (
	"context"
	"log/slog"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	"github.com/caelis-labs/caelis/internal/controlplane"
)

// runtimeHostAuthorities is the immutable set of process services borrowed by
// the Host root and detached Session Runtime instances. Copying this value
// copies references only; Runtime compositions never own these lifecycles.
type runtimeHostAuthorities struct {
	appName string
	userID  string
	// store is used only as a configuration reader in detached Runtime values;
	// only the Host root uses the write-hooked store instance.
	store                  *appConfigStore
	storeDir               string
	diagnostics            *slog.Logger
	configMigration        configstore.MigrationReport
	fenceOwnerID           string
	priorHostSessionFences controlplane.PriorHostFenceReplacer
	taskStore              task.Store
	controlFeeds           appserver.FeedRegistry
	approvalRecovery       *appserver.ApprovalRecoveryGate
	codexAuth              *codexauth.Manager
	grokAuth               *grokauth.Manager
	apiKeyCredentials      *credentialstore.Store
	providerUsage          *providerusage.Registry
	sessionModelPins       *sessionModelPinRegistry
	lifecycleCtx           context.Context
	// hostedChildInput is the Host-owned parent/sibling route borrowed by
	// spawned child Session Runtimes. The function carries routing capability,
	// not Registry ownership.
	hostedChildInput hostedChildInputFunc
}
