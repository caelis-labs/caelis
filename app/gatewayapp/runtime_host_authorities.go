package gatewayapp

import (
	"context"
	"log/slog"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/memoryhost"
	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

type runtimeMemoryHost interface {
	ValidateBinding(memorybinding.RuntimeMemoryBindingSnapshot) error
	ValidateAuthority(context.Context, memorybinding.RuntimeMemoryBindingSnapshot) error
	Bind(memorybinding.RuntimeMemoryBindingSnapshot, v1alpha1.SourceContext, v1alpha1.RecallBudget) (memoryhost.BoundClient, error)
}

// runtimeHostAuthorities is the immutable set of process services borrowed by
// the Host root and detached Session Runtime instances. Copying this value
// copies references only; Runtime compositions never own these lifecycles.
type runtimeHostAuthorities struct {
	appName string
	userID  string
	// store is used only as a configuration reader in detached Runtime values;
	// only the Host root uses the write-hooked store instance.
	store                   *appConfigStore
	storeDir                string
	sandboxHostAuthorityDir string
	diagnostics             *slog.Logger
	configMigration         configstore.MigrationReport
	fenceOwnerID            string
	taskStore               task.Store
	controlFeeds            appserver.FeedRegistry
	approvalRecovery        *appserver.ApprovalRecoveryGate
	codexAuth               *codexauth.Manager
	grokAuth                *grokauth.Manager
	apiKeyCredentials       *credentialstore.Store
	providerUsage           *providerusage.Registry
	adapterHost             controladapterhost.Service
	acpEndpointResolver     endpoint.Resolver
	sessionModelPins        *sessionModelPinRegistry
	memoryHost              runtimeMemoryHost
	lifecycleCtx            context.Context
	// hostedChildInput is the Host-owned parent/sibling route borrowed by
	// spawned child Session Runtimes. The function carries routing capability,
	// not Registry ownership.
	hostedChildInput hostedChildInputFunc
}
