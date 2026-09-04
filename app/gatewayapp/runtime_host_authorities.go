package gatewayapp

import (
	"context"
	"log/slog"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/memoryhost"
	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	"github.com/caelis-labs/caelis/control/streamspool"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

type runtimeMemoryHost interface {
	ValidateBinding(memorybinding.RuntimeMemoryBindingSnapshot) error
	ValidateAuthority(context.Context, memorybinding.RuntimeMemoryBindingSnapshot) error
	Bind(memorybinding.RuntimeMemoryBindingSnapshot, v1alpha1.SourceContext, v1alpha1.RecallBudget) (memoryhost.BoundClient, error)
}

// taskOutputLifecycle is the Control-owned close side of the producer-only SDK
// binding. Keeping it separate prevents Agent Runtime from acquiring replay,
// retention, or product-address lifecycle authority.
type taskOutputLifecycle interface {
	ReleaseTask(context.Context, task.Ref) error
	ReleaseSession(context.Context, session.SessionRef) error
	Close(context.Context) error
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
	taskOutput              output.Binder
	taskOutputLifecycle     taskOutputLifecycle
	streamSpool             streamspool.Store
	controlFeeds            appserver.FeedRegistry
	controlFeedLifecycle    appserver.FeedRegistryLifecycle
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
