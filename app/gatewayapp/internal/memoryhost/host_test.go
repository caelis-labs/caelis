package memoryhost

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/memorybinding"
	managementv1alpha1 "github.com/caelis-labs/memory/api/memory/management/v1alpha1"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

func TestEmbeddedHostBindsSDKClient(t *testing.T) {
	ctx := context.Background()
	credentials := map[string]string{}
	host, err := Open(ctx, Config{
		DataDir: t.TempDir(),
		Credentials: func(_ context.Context, ref string) (string, error) {
			return credentials[ref], nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	bootstrap, err := host.Management().Bootstrap(ctx, managementv1alpha1.BootstrapRequest{
		Realms:     []managementv1alpha1.Realm{{ID: "realm:test"}},
		Identities: []managementv1alpha1.Identity{{ID: "identity:test", RealmID: "realm:test"}},
		Spaces: []managementv1alpha1.Space{{
			ID: "space:test", RealmID: "realm:test", IdentityID: "identity:test", Class: v1alpha1.SpaceClassPrivate,
		}},
		Views: []managementv1alpha1.ViewDefinition{{
			ID: "view:test", RealmID: "realm:test", ReadSpaceIDs: []v1alpha1.SpaceID{"space:test"},
			WriteSpaceID: "space:test", MaxDisclosureClass: v1alpha1.SpaceClassPrivate, Version: 1,
		}},
		Grants: []managementv1alpha1.Grant{{
			ID: "grant:test", PrincipalRef: "principal:test", ActorRef: "actor:test", ViewRef: "view:test",
			AllowedOperations: []v1alpha1.Operation{
				v1alpha1.OperationRemember,
				v1alpha1.OperationRecall,
				v1alpha1.OperationReceiptStatus,
			},
			AllowedAudiences: []v1alpha1.Audience{v1alpha1.AudiencePrivate},
			ExpiresAt:        time.Now().Add(time.Hour), Version: 1,
		}},
		IssuerPrincipals: []string{"principal:test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials["issuer:test"] = bootstrap.IssuerCredentials["principal:test"]
	binding := memorybinding.RuntimeMemoryBindingSnapshot{
		BindingRef: "binding:test", RuntimeActorRef: "actor:test", PrincipalRef: "principal:test",
		IssuerCredentialRef: "issuer:test", ViewRef: "view:test", GrantRef: "grant:test",
		Audience: memorybinding.OutputAudiencePrivate, BindingVersion: 1,
		Labels: v1alpha1.LabelSet{"workspace:alpha"},
	}
	if err := host.ValidateAuthority(ctx, binding); err != nil {
		t.Fatalf("ValidateAuthority() = %v", err)
	}
	for name, mutate := range map[string]func(*memorybinding.RuntimeMemoryBindingSnapshot){
		"credential": func(binding *memorybinding.RuntimeMemoryBindingSnapshot) {
			binding.IssuerCredentialRef = "issuer:missing"
		},
		"principal": func(binding *memorybinding.RuntimeMemoryBindingSnapshot) { binding.PrincipalRef = "principal:other" },
		"grant":     func(binding *memorybinding.RuntimeMemoryBindingSnapshot) { binding.GrantRef = "grant:missing" },
		"actor":     func(binding *memorybinding.RuntimeMemoryBindingSnapshot) { binding.RuntimeActorRef = "actor:other" },
		"audience": func(binding *memorybinding.RuntimeMemoryBindingSnapshot) {
			binding.Audience = memorybinding.OutputAudienceShared
		},
	} {
		t.Run("authority-"+name, func(t *testing.T) {
			changed := binding
			mutate(&changed)
			if err := host.ValidateAuthority(ctx, changed); err == nil {
				t.Fatal("ValidateAuthority() accepted invalid authority")
			}
		})
	}
	client, err := host.Bind(binding, v1alpha1.SourceContext{
		ActorRef: "actor:test", SessionRef: "session:test", SourceType: "test",
	}, v1alpha1.RecallBudget{MaxFragments: 8, MaxBytes: 16 << 10, DeadlineMS: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	remembered, err := client.Remember(ctx, "embedded memory host", "remember:test", nil)
	if err != nil || !remembered.Accepted {
		t.Fatalf("Remember() = %#v, %v", remembered, err)
	}
	recalled, err := client.Recall(ctx, "embedded host", remembered.ConsistencyToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled.Fragments) != 1 || recalled.Fragments[0].Text != "embedded memory host" {
		t.Fatalf("Recall() = %#v", recalled)
	}
	otherBinding := binding
	otherBinding.Labels = v1alpha1.LabelSet{"workspace:beta"}
	other, err := host.Bind(otherBinding, v1alpha1.SourceContext{
		ActorRef: "actor:test", SessionRef: "session:other", SourceType: "test",
	}, v1alpha1.RecallBudget{MaxFragments: 8, MaxBytes: 16 << 10, DeadlineMS: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := other.Recall(ctx, "embedded host", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(isolated.Fragments) != 0 {
		t.Fatalf("other Runtime labels recalled memory: %#v", isolated.Fragments)
	}
	if _, err := other.GetReceiptStatus(ctx, remembered.ReceiptID); !v1alpha1.IsCode(err, v1alpha1.ErrorCodeNotFound) {
		t.Fatalf("other Runtime labels ReceiptStatus error = %v, want not_found", err)
	}
}

func TestEmbeddedHostRejectsIncompleteBinding(t *testing.T) {
	host, err := Open(t.Context(), Config{
		DataDir:     t.TempDir(),
		Credentials: func(context.Context, string) (string, error) { return "unused", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if err := host.ValidateBinding(memorybinding.RuntimeMemoryBindingSnapshot{}); err == nil {
		t.Fatal("ValidateBinding() accepted an incomplete logical binding")
	}
}

func TestEmbeddedHostCloseIsSafeWithConcurrentAccess(t *testing.T) {
	host, err := Open(t.Context(), Config{
		DataDir:     t.TempDir(),
		Credentials: func(context.Context, string) (string, error) { return "unused", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for range 100 {
				_ = host.Management()
				_ = host.StewardWorker()
				_ = host.ValidateBinding(memorybinding.RuntimeMemoryBindingSnapshot{})
				_, _ = host.Bind(memorybinding.RuntimeMemoryBindingSnapshot{}, v1alpha1.SourceContext{}, v1alpha1.RecallBudget{})
			}
		}()
	}
	close(start)
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if host.Management() != nil || host.StewardWorker() != nil {
		t.Fatal("closed host still exposed Memory service planes")
	}
	if err := host.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
}
