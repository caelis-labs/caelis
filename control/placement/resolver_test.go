package placement

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestResolveHandleFreezesExactACPPlacement(t *testing.T) {
	snapshot := testSnapshot()
	got, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleOrbit, Purpose: PurposeSpawn})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != sdkplacement.KindAgent || got.ProfileID != "acp:claude:opus" || got.Agent != "claude" || got.Model != "Opus-V4" || got.ReasoningEffort != "xhigh" {
		t.Fatalf("Resolve(orbit) = %#v", got)
	}
	if got.ReasoningEffortConfigID != "thought_level" {
		t.Fatalf("ReasoningEffortConfigID = %q, want thought_level", got.ReasoningEffortConfigID)
	}
	wantValues := map[string]string{"mode": "code", "thought_level": "very-high"}
	if !reflect.DeepEqual(got.SessionConfigValues, wantValues) {
		t.Fatalf("SessionConfigValues = %#v, want %#v", got.SessionConfigValues, wantValues)
	}
	if err := sdkplacement.ValidateSealed(got); err != nil {
		t.Fatalf("ValidateSealed() error = %v", err)
	}
}

func TestPurposeForHandleMapsDirectAndSystemInvocations(t *testing.T) {
	for _, test := range []struct {
		handle agentbinding.Handle
		want   Purpose
	}{
		{handle: agentbinding.HandleBreeze, want: PurposeDirect},
		{handle: agentbinding.HandleOrbit, want: PurposeDirect},
		{handle: agentbinding.HandleZenith, want: PurposeDirect},
		{handle: agentbinding.HandleGuardian, want: PurposeGuardian},
		{handle: agentbinding.HandleReviewer, want: PurposeReviewer},
	} {
		got, err := PurposeForFixedHandle(test.handle)
		if err != nil || got != test.want {
			t.Errorf("PurposeForFixedHandle(%q) = %q, %v; want %q", test.handle, got, err, test.want)
		}
	}
	for _, handle := range []agentbinding.Handle{agentbinding.HandleSelf, "unknown"} {
		if _, err := PurposeForFixedHandle(handle); err == nil {
			t.Errorf("PurposeForFixedHandle(%q) succeeded, want rejection", handle)
		}
	}
}

func TestCustomRoleResolvesForSpawnAndDirectInvocation(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Bindings.Roles = []agentbinding.Role{{
		Handle: "research", Description: "Investigate unfamiliar systems.",
	}}
	snapshot.Bindings.Bindings = append(snapshot.Bindings.Bindings, agentbinding.Binding{
		Handle: "research", ProfileID: "acp:claude:opus", Effort: "xhigh",
	})
	purpose, err := PurposeForHandle(agentbinding.CatalogFor(snapshot.Bindings), "research")
	if err != nil || purpose != PurposeDirect {
		t.Fatalf("PurposeForHandle(research) = %q, %v", purpose, err)
	}
	for _, purpose := range []Purpose{PurposeSpawn, PurposeDirect} {
		got, err := ResolveHandle(snapshot, HandleRequest{Handle: "research", Purpose: purpose})
		if err != nil {
			t.Fatalf("ResolveHandle(research, %s) error = %v", purpose, err)
		}
		if got.Kind != sdkplacement.KindAgent || got.ProfileID != "acp:claude:opus" {
			t.Fatalf("ResolveHandle(research, %s) = %#v", purpose, got)
		}
	}
}

func TestProviderAndSelfResolveToSealedModelPlacement(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Bindings.Bindings = snapshot.Bindings.Bindings[:1]
	guardian, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleGuardian, Purpose: PurposeGuardian})
	if err != nil {
		t.Fatal(err)
	}
	if guardian.Kind != sdkplacement.KindModel || guardian.ProfileID != "provider:main" || guardian.Model != "provider/model" || guardian.ReasoningEffort != "high" {
		t.Fatalf("Resolve(guardian) = %#v", guardian)
	}
	self, err := ResolveHandle(snapshot, HandleRequest{
		Handle: agentbinding.HandleSelf, Purpose: PurposeSpawn,
		Session: SessionContext{ProfileID: "provider:main", Effort: "low"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if self.ProfileID != "provider:main" || self.ReasoningEffort != "low" || self.Fingerprint == guardian.Fingerprint {
		t.Fatalf("Resolve(self) = %#v, guardian = %#v", self, guardian)
	}
}

func TestSelfRejectsMissingSessionProfile(t *testing.T) {
	_, err := ResolveHandle(testSnapshot(), HandleRequest{
		Handle: agentbinding.HandleSelf, Purpose: PurposeSpawn,
		Session: SessionContext{ProfileID: "provider:missing", Effort: "none"},
	})
	if err == nil || !strings.Contains(err.Error(), `unknown profile "provider:missing"`) {
		t.Fatalf("ResolveHandle(self missing profile) error = %v, want fail-closed rejection", err)
	}
}

func TestUnboundSystemAgentUsesAnEligibleDefaultProfile(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Bindings.Bindings = snapshot.Bindings.Bindings[:1]
	snapshot.Profiles.DefaultProfileID = ""
	_, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleReviewer, Purpose: PurposeReviewer})
	var unavailable *DefaultProfileError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Resolve(missing default) error = %v, want DefaultProfileError", err)
	}

	snapshot = testSnapshot()
	snapshot.Bindings.Bindings = snapshot.Bindings.Bindings[:1]
	snapshot.Profiles.DefaultProfileID = "acp:claude:opus"
	_, err = ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleGuardian, Purpose: PurposeGuardian})
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "cannot use its acp backend") {
		t.Fatalf("Resolve(Guardian ACP default) error = %v, want typed backend rejection", err)
	}
	reviewer, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleReviewer, Purpose: PurposeReviewer})
	if err != nil {
		t.Fatalf("Resolve(Reviewer ACP default) error = %v", err)
	}
	if reviewer.Kind != sdkplacement.KindAgent || reviewer.Agent != "claude" || reviewer.Model != "Opus-V4" {
		t.Fatalf("Resolve(Reviewer ACP default) = %#v", reviewer)
	}
}

func TestParticipantResolvesExplicitACPProfileWithoutHandleBinding(t *testing.T) {
	got, err := ResolveParticipant(testSnapshot(), " ACP:Claude:Opus ", "very-high")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != sdkplacement.KindAgent || got.ProfileID != "acp:claude:opus" || got.Agent != "claude" || got.Model != "Opus-V4" || got.ReasoningEffort != "xhigh" {
		t.Fatalf("Resolve(participant) = %#v", got)
	}
	if err := sdkplacement.ValidateSealed(got); err != nil {
		t.Fatalf("ValidateSealed() error = %v", err)
	}
}

func TestResolveProfileSupportsProviderAndACPMainExecution(t *testing.T) {
	snapshot := testSnapshot()
	provider, err := ResolveProfile(snapshot, "provider:main", "low")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Kind != sdkplacement.KindModel || provider.ProfileID != "provider:main" || provider.ReasoningEffort != "low" {
		t.Fatalf("ResolveProfile(provider) = %#v", provider)
	}
	acp, err := ResolveProfile(snapshot, "acp:claude:opus", "very-high")
	if err != nil {
		t.Fatal(err)
	}
	if acp.Kind != sdkplacement.KindAgent || acp.Agent != "claude" || acp.Model != "Opus-V4" || acp.ReasoningEffort != "xhigh" {
		t.Fatalf("ResolveProfile(ACP) = %#v", acp)
	}
	for _, placed := range []sdkplacement.Placement{provider, acp} {
		if err := sdkplacement.ValidateSealed(placed); err != nil {
			t.Fatalf("ValidateSealed(%#v) = %v", placed, err)
		}
	}
}

func TestParticipantRejectsProviderProfileAndMissingSelection(t *testing.T) {
	snapshot := testSnapshot()
	for _, selection := range []struct {
		profileID string
		effort    string
	}{
		{profileID: "provider:main", effort: "high"},
		{profileID: "acp:missing", effort: "high"},
		{profileID: "acp:claude:opus", effort: "low"},
		{profileID: "acp:claude:opus"},
	} {
		_, err := ResolveParticipant(snapshot, selection.profileID, selection.effort)
		var selectionErr *ParticipantSelectionError
		if !errors.As(err, &selectionErr) {
			t.Fatalf("ResolveParticipant(%q, %q) error = %v, want ParticipantSelectionError", selection.profileID, selection.effort, err)
		}
	}
}

func TestParticipantSnapshotFailureIsNotASelectionError(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Agents.Connections = nil
	_, err := ResolveParticipant(snapshot, "acp:claude:opus", "xhigh")
	var selectionErr *ParticipantSelectionError
	if err == nil || errors.As(err, &selectionErr) {
		t.Fatalf("ResolveParticipant(invalid snapshot) error = %v, want untyped internal snapshot failure", err)
	}
}

func TestPreparedPlacementChangesWhenReferencedConfigurationChanges(t *testing.T) {
	first := testSnapshot()
	prepared, err := ResolveHandle(first, HandleRequest{Handle: agentbinding.HandleGuardian, Purpose: PurposeGuardian})
	if err != nil {
		t.Fatal(err)
	}
	changed := testSnapshot()
	changed.Models[0].MaxOutputTok = 16384
	next, err := ResolveHandle(changed, HandleRequest{Handle: agentbinding.HandleGuardian, Purpose: PurposeGuardian})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ConfigFingerprint == next.ConfigFingerprint || prepared.Fingerprint == next.Fingerprint {
		t.Fatalf("configuration change did not invalidate placement:\nprepared %#v\nnext %#v", prepared, next)
	}
}

func TestValidateFrozenIgnoresHandleRebindingButRejectsBackendMutation(t *testing.T) {
	snapshot := testSnapshot()
	prepared, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleOrbit, Purpose: PurposeSpawn})
	if err != nil {
		t.Fatal(err)
	}
	rebound := testSnapshot()
	rebound.Bindings.Bindings[0] = agentbinding.Binding{
		Handle: agentbinding.HandleOrbit, ProfileID: "provider:main", Effort: "low",
	}
	if err := ValidateFrozen(rebound, prepared); err != nil {
		t.Fatalf("ValidateFrozen(rebound handle) error = %v", err)
	}
	mutated := testSnapshot()
	mutated.Profiles.Profiles[1].Backend.ACP.RemoteModelID = "Opus-V5"
	if err := ValidateFrozen(mutated, prepared); err == nil || !strings.Contains(err.Error(), "changed after placement was frozen") {
		t.Fatalf("ValidateFrozen(mutated backend) error = %v", err)
	}
}

func TestReviewerACPBindingResolvesWhileGuardianFailsClosed(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Bindings.Bindings = append(snapshot.Bindings.Bindings, agentbinding.Binding{
		Handle: agentbinding.HandleReviewer, ProfileID: "acp:claude:opus", Effort: "xhigh",
	})
	reviewer, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleReviewer, Purpose: PurposeReviewer})
	if err != nil {
		t.Fatalf("Resolve(ACP reviewer) error = %v", err)
	}
	if reviewer.Kind != sdkplacement.KindAgent || reviewer.ProfileID != "acp:claude:opus" || reviewer.Agent != "claude" {
		t.Fatalf("Resolve(ACP reviewer) = %#v", reviewer)
	}

	snapshot = testSnapshot()
	snapshot.Bindings.Bindings[1] = agentbinding.Binding{Handle: agentbinding.HandleGuardian, ProfileID: "acp:claude:opus", Effort: "xhigh"}
	_, err = ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleGuardian, Purpose: PurposeGuardian})
	var unsupported *agentbinding.UnsupportedBackendError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Resolve(ACP guardian) error = %v, want UnsupportedBackendError", err)
	}
}

func TestResolveRejectsUnboundAndPurposeMismatch(t *testing.T) {
	snapshot := testSnapshot()
	if _, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleBreeze, Purpose: PurposeSpawn}); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("Resolve(unbound) error = %v", err)
	}
	if _, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleSelf, Purpose: PurposeDirect, Session: SessionContext{ProfileID: "provider:main", Effort: "high"}}); err == nil || !strings.Contains(err.Error(), "not directly runnable") {
		t.Fatalf("Resolve(direct self) error = %v", err)
	}
}

func testSnapshot() Snapshot {
	return Snapshot{
		Profiles: modelprofile.Configuration{
			DefaultProfileID: "provider:main",
			Profiles: []modelprofile.ModelProfile{
				{
					ID: "provider:main", DisplayName: "Provider Main",
					Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "provider/model"}},
					Effort:  modelprofile.EffortCapability{DefaultEffort: "high", Choices: []modelprofile.EffortChoice{{Canonical: "low", WireValue: "low"}, {Canonical: "high", WireValue: "high"}}},
				},
				{
					ID: "acp:claude:opus", DisplayName: "Claude Opus",
					Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{
						AgentID: "claude", RemoteModelID: "Opus-V4", SessionDefaults: map[string]string{"mode": "code"},
					}},
					Effort: modelprofile.EffortCapability{
						DefaultEffort: "xhigh", ACPConfigID: "thought_level",
						Choices: []modelprofile.EffortChoice{{Canonical: "high", WireValue: "high"}, {Canonical: "xhigh", WireValue: "very-high"}},
					},
				},
			},
		},
		Bindings: agentbinding.Configuration{Bindings: []agentbinding.Binding{
			{Handle: agentbinding.HandleOrbit, ProfileID: "acp:claude:opus", Effort: "xhigh"},
			{Handle: agentbinding.HandleGuardian, ProfileID: "provider:main", Effort: "high"},
		}},
		Models: []modelconfig.Config{{
			ID:                 "provider/model",
			ProviderEndpointID: "provider",
			Alias:              "model",
			ReasoningMode:      "effort", ReasoningLevels: []string{"low", "high"}, DefaultReasoningEffort: "high",
		}},
		Agents: controlagents.Configuration{
			Connections: []controlagents.Connection{{
				ID: "claude", Name: "Claude", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindExecutable, Command: "claude-agent"},
			}},
			Agents: []controlagents.Agent{{ID: "claude", Name: "Claude", ConnectionID: "claude"}},
		},
	}
}
