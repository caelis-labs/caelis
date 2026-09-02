package gatewayapp

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/memoryhost"
	"github.com/caelis-labs/caelis/control/memorybinding"
	memorycredentialstore "github.com/caelis-labs/caelis/control/memorybinding/credentialstore"
	managementv1alpha1 "github.com/caelis-labs/memory/api/memory/management/v1alpha1"
	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

const (
	defaultMemoryRealmID      = "realm:caelis-local"
	defaultMemoryIdentityID   = "identity:caelis-local"
	defaultMemorySpaceID      = "space:caelis-local-private"
	defaultMemoryViewID       = "view:caelis-local-private"
	defaultMemoryGrantID      = "grant:caelis-local-private"
	defaultMemoryPrincipal    = "principal:caelis-local"
	defaultMemoryRuntimeActor = "actor:caelis-local"
	defaultMemoryBindingRef   = "local-private"
)

type preparedEmbeddedMemory struct {
	document        AppConfig
	host            *memoryhost.Host
	defaultTopology bool
}

// prepareEmbeddedMemory synchronously opens the built-in Memory package and
// provisions its private default topology on first Host startup.
func prepareEmbeddedMemory(
	ctx context.Context,
	config Config,
	store *appConfigStore,
	storeDir string,
	document AppConfig,
) (preparedEmbeddedMemory, error) {
	prepared := preparedEmbeddedMemory{document: document}
	if config.memoryHost != nil {
		return prepared, nil
	}
	credentials, err := memorycredentialstore.New(storeDir)
	if err != nil {
		return prepared, err
	}
	host, err := memoryhost.Open(ctx, memoryhost.Config{
		DataDir:     filepath.Join(storeDir, "memory", "appliance"),
		Credentials: credentials.Get,
	})
	if err != nil {
		return prepared, err
	}
	prepared.host = host
	if memorybinding.IsConfigured(document.Memory) {
		prepared.defaultTopology = isDefaultEmbeddedMemoryConfiguration(document.Memory)
		return prepared, nil
	}

	provisioned, err := provisionDefaultMemoryTopology(ctx, store, credentials, document, host)
	if err != nil {
		_ = host.Close()
		prepared.host = nil
		return prepared, err
	}
	prepared.document = provisioned
	prepared.defaultTopology = true
	return prepared, nil
}

func isDefaultEmbeddedMemoryConfiguration(configuration memorybinding.Configuration) bool {
	configuration = memorybinding.Normalize(configuration)
	if configuration.DefaultBindingRef != defaultMemoryBindingRef || len(configuration.Bindings) != 1 {
		return false
	}
	binding := configuration.Bindings[0]
	return binding.BindingRef == defaultMemoryBindingRef &&
		binding.RuntimeActorRef == defaultMemoryRuntimeActor &&
		binding.PrincipalRef == defaultMemoryPrincipal &&
		binding.ViewRef == defaultMemoryViewID &&
		binding.GrantRef == defaultMemoryGrantID
}

func provisionDefaultMemoryTopology(
	ctx context.Context,
	store *appConfigStore,
	credentials *memorycredentialstore.Store,
	document AppConfig,
	host *memoryhost.Host,
) (AppConfig, error) {
	if store == nil || credentials == nil || host == nil {
		return AppConfig{}, fmt.Errorf("gatewayapp: default Memory provisioning is unavailable")
	}
	admin := host.Management()
	if admin == nil {
		return AppConfig{}, fmt.Errorf("gatewayapp: embedded Memory management is unavailable")
	}

	operations := []memoryv1alpha1.Operation{
		memoryv1alpha1.OperationRemember,
		memoryv1alpha1.OperationRecall,
		memoryv1alpha1.OperationReceiptStatus,
	}
	bootstrap := managementv1alpha1.BootstrapRequest{
		Realms:     []managementv1alpha1.Realm{{ID: defaultMemoryRealmID}},
		Identities: []managementv1alpha1.Identity{{ID: defaultMemoryIdentityID, RealmID: defaultMemoryRealmID}},
		Spaces: []managementv1alpha1.Space{{
			ID: defaultMemorySpaceID, RealmID: defaultMemoryRealmID,
			IdentityID: defaultMemoryIdentityID, Class: memoryv1alpha1.SpaceClassPrivate,
		}},
		Views: []managementv1alpha1.ViewDefinition{{
			ID: defaultMemoryViewID, RealmID: defaultMemoryRealmID,
			ReadSpaceIDs: []memoryv1alpha1.SpaceID{defaultMemorySpaceID}, WriteSpaceID: defaultMemorySpaceID,
			MaxDisclosureClass: memoryv1alpha1.SpaceClassPrivate, Version: 1,
		}},
		Grants: []managementv1alpha1.Grant{{
			ID: defaultMemoryGrantID, PrincipalRef: defaultMemoryPrincipal, ActorRef: defaultMemoryRuntimeActor,
			ViewRef: defaultMemoryViewID, AllowedOperations: operations,
			AllowedAudiences: []memoryv1alpha1.Audience{memoryv1alpha1.AudiencePrivate},
			ExpiresAt:        time.Now().UTC().AddDate(20, 0, 0), Version: 1,
		}},
		IssuerPrincipals: []string{defaultMemoryPrincipal},
	}
	if _, err := admin.Bootstrap(ctx, bootstrap); err != nil {
		inspection, inspectErr := admin.Inspect(ctx)
		if inspectErr != nil || !inspectionContainsSpace(inspection, defaultMemorySpaceID) {
			return AppConfig{}, fmt.Errorf("gatewayapp: bootstrap default Memory topology: %w", err)
		}
	}

	issuer, err := admin.RotateIssuerCredential(ctx, defaultMemoryPrincipal)
	if err != nil {
		return AppConfig{}, fmt.Errorf("gatewayapp: establish default Memory issuer: %w", err)
	}
	issuerRef := memorycredentialstore.BuildReference(defaultMemoryPrincipal)
	if err := credentials.Put(ctx, issuerRef, issuer.Credential); err != nil {
		return AppConfig{}, fmt.Errorf("gatewayapp: persist default Memory issuer: %w", err)
	}

	candidate := document
	candidate.Memory = memorybinding.Configuration{
		DefaultBindingRef: defaultMemoryBindingRef,
		Bindings: []memorybinding.AccessBinding{{
			BindingRef: defaultMemoryBindingRef, RuntimeActorRef: defaultMemoryRuntimeActor,
			PrincipalRef: defaultMemoryPrincipal, IssuerCredentialRef: issuerRef,
			ViewRef: defaultMemoryViewID, GrantRef: defaultMemoryGrantID,
			Audience: memorybinding.OutputAudiencePrivate, BindingVersion: 1,
		}},
	}
	saved, err := store.CompareAndSave(ctx, document.ConfigurationRevision, candidate)
	if err != nil {
		if configstore.WriteCommitted(err) && saved.ConfigurationRevision != 0 {
			return saved, nil
		}
		return AppConfig{}, fmt.Errorf("gatewayapp: publish default Memory binding: %w", err)
	}
	return saved, nil
}

func inspectionContainsSpace(inspection managementv1alpha1.Inspection, expected memoryv1alpha1.SpaceID) bool {
	for _, space := range inspection.Spaces {
		if space.ID == expected && space.IdentityID == defaultMemoryIdentityID && space.Class == memoryv1alpha1.SpaceClassPrivate {
			return true
		}
	}
	return false
}
