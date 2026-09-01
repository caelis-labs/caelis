package memorybinding

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveSelectsOneAudienceIntoDetachedRuntimeSnapshot(t *testing.T) {
	configuration := testConfiguration()
	private, enabled, err := Resolve(configuration, RuntimeSelection{
		BotID: " bot-a ", Audience: " PRIVATE ",
	}, false)
	if err != nil || !enabled {
		t.Fatalf("Resolve(private) = %#v, %v, %v", private, enabled, err)
	}
	if private.RuntimeActorRef != "actor-a" || private.PrincipalRef != "principal:a" ||
		private.ViewRef != "view-a-private" || private.GrantRef != "grant-a-private" ||
		private.Audience != OutputAudiencePrivate || private.BindingVersion != 7 {
		t.Fatalf("private snapshot = %#v", private)
	}
	if strings.Contains(private.ViewRef, "shared") || strings.Contains(private.GrantRef, "shared") {
		t.Fatalf("private snapshot retained alternate audience binding: %#v", private)
	}

	shared, enabled, err := Resolve(configuration, RuntimeSelection{
		BotID: "bot-a", Audience: OutputAudienceShared,
	}, false)
	if err != nil || !enabled {
		t.Fatalf("Resolve(shared) = %#v, %v, %v", shared, enabled, err)
	}
	if shared.ViewRef != "view-a-shared" || shared.GrantRef != "grant-a-shared" || shared.Audience != OutputAudienceShared {
		t.Fatalf("shared snapshot = %#v", shared)
	}
	if shared == private {
		t.Fatal("private and shared selections produced the same snapshot")
	}
}

func TestFeatureDisableAndKillSwitchPreserveConfiguration(t *testing.T) {
	configuration := testConfiguration()
	disabled := configuration
	disabled.Enabled = false
	if err := Validate(disabled); err != nil {
		t.Fatalf("Validate(disabled retained config) = %v", err)
	}
	if snapshot, enabled, err := Resolve(disabled, RuntimeSelection{BotID: "bot-a", Audience: OutputAudiencePrivate}, false); err != nil || enabled || snapshot != (RuntimeMemoryBindingSnapshot{}) {
		t.Fatalf("Resolve(feature disabled) = %#v, %v, %v", snapshot, enabled, err)
	}
	if !reflect.DeepEqual(disabled.Endpoint, configuration.Endpoint) || !reflect.DeepEqual(disabled.Bots, configuration.Bots) {
		t.Fatal("feature disable changed retained binding references")
	}
	if snapshot, enabled, err := Resolve(configuration, RuntimeSelection{BotID: "bot-a", Audience: OutputAudiencePrivate}, true); err != nil || enabled || snapshot != (RuntimeMemoryBindingSnapshot{}) {
		t.Fatalf("Resolve(kill switch) = %#v, %v, %v", snapshot, enabled, err)
	}
}

func TestValidationRejectsDuplicateOrIncompleteAuthorityReferences(t *testing.T) {
	for name, mutate := range map[string]func(*Configuration){
		"duplicate Bot": func(configuration *Configuration) {
			duplicate := configuration.Bots[0]
			duplicate.RuntimeActorRef = "actor-b"
			configuration.Bots = append(configuration.Bots, duplicate)
		},
		"duplicate actor": func(configuration *Configuration) {
			duplicate := configuration.Bots[0]
			duplicate.BotID = "bot-b"
			configuration.Bots = append(configuration.Bots, duplicate)
		},
		"half private binding": func(configuration *Configuration) {
			configuration.Bots[0].Private.GrantRef = ""
		},
		"missing issuer credential reference": func(configuration *Configuration) {
			configuration.Bots[0].IssuerCredentialRef = ""
		},
		"persisted managed endpoint": func(configuration *Configuration) {
			configuration.Endpoint.Endpoint = "/tmp/memoryd.sock"
		},
		"short artifact digest": func(configuration *Configuration) {
			configuration.Endpoint.Compatibility.ArtifactSHA256 = "abc"
		},
	} {
		t.Run(name, func(t *testing.T) {
			configuration := testConfiguration()
			mutate(&configuration)
			if err := Validate(configuration); err == nil {
				t.Fatalf("Validate(%s) succeeded: %#v", name, configuration)
			}
		})
	}
}

func TestResolveFailsClosedForUnknownBotAudienceOrUnboundAudience(t *testing.T) {
	configuration := testConfiguration()
	for name, selection := range map[string]RuntimeSelection{
		"unknown Bot":      {BotID: "bot-b", Audience: OutputAudiencePrivate},
		"unknown audience": {BotID: "bot-a", Audience: "public"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, enabled, err := Resolve(configuration, selection, false); err == nil || enabled {
				t.Fatalf("Resolve(%s) = enabled:%v error:%v", name, enabled, err)
			}
		})
	}
	configuration.Bots[0].Shared = AudienceBinding{}
	if _, enabled, err := Resolve(configuration, RuntimeSelection{BotID: "bot-a", Audience: OutputAudienceShared}, false); err == nil || enabled {
		t.Fatalf("Resolve(unbound shared) = enabled:%v error:%v", enabled, err)
	}
}

func testConfiguration() Configuration {
	return Configuration{
		Enabled: true,
		Endpoint: EndpointConfig{
			ID: "memory-default", Deployment: DeploymentModeManagedLocal,
			Compatibility: APICompatibility{
				Protocol: "memory.local.v1alpha1", APIVersion: "memory.v1alpha1",
				CoreProfile: "memory.core.v1alpha1", ServiceVersion: "0.2.0-alpha.1",
				BuildRevision: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64),
			},
		},
		Bots: []BotMemoryBinding{{
			BotID: "bot-a", RuntimeActorRef: "actor-a", MemoryIdentityRef: "identity-a",
			PrincipalRef: "principal:a", IssuerCredentialRef: "memory-issuer:bot-a",
			Private:        AudienceBinding{ViewRef: "view-a-private", GrantRef: "grant-a-private"},
			Shared:         AudienceBinding{ViewRef: "view-a-shared", GrantRef: "grant-a-shared"},
			BindingVersion: 7,
		}},
	}
}
