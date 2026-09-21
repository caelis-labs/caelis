package gatewayapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/memorytool"
	managementv1alpha1 "github.com/caelis-labs/memory/api/memory/management/v1alpha1"
	stewardv1alpha1 "github.com/caelis-labs/memory/api/memory/steward/v1alpha1"
	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

// TestMemoryStewardConsumerProfileUpgrade is the consumer regression for the
// Memory v0.6.1 Steward policy upgrade: a Host whose durable appliance already
// holds the historical memory-default@1 profile plus a Job captured against it
// must, on reopen, bind the current built-in version, retain the historical
// profile verbatim, drain the old Job, organize new work with the current
// profile, and restart idempotently. It covers the Host syncPolicy/Worker route
// only; Memory's own schema migration is upstream's database-fixture test.
func TestMemoryStewardConsumerProfileUpgrade(t *testing.T) {
	for _, release := range []string{"v0.5.2", "v0.6.0"} {
		t.Run(release, func(t *testing.T) {
			runMemoryStewardConsumerProfileUpgrade(t, release)
		})
	}
}

func runMemoryStewardConsumerProfileUpgrade(t *testing.T, release string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	historical := loadHistoricalStewardProfile(t, release)
	if defaultMemoryStewardProfile.Version <= historical.Version {
		t.Fatalf("current built-in profile version %d must exceed historical %d", defaultMemoryStewardProfile.Version, historical.Version)
	}
	root := t.TempDir()
	storeDir := filepath.Join(root, "caelis")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := newMemoryGoldenProvider(t)
	provider.EnableSteward()

	// Seed a pre-upgrade Host. Stop its bridge first so binding the Steward
	// model cannot register the current profile, then persist the historical
	// profile/binding through the public owner APIs.
	stack := newMemoryGoldenStack(t, provider, storeDir, workspace)
	if stack.memorySteward == nil {
		t.Fatal("Host started without a Memory Steward bridge")
	}
	if err := stack.memorySteward.wait(ctx); err != nil {
		t.Fatalf("stop Memory Steward bridge: %v", err)
	}
	admin := stack.memoryRuntime.Management()
	seeded, err := admin.PutStewardProfile(ctx, managementv1alpha1.PutStewardProfileRequest{Profile: historical})
	if err != nil || !seeded.Created {
		t.Fatalf("seed historical Steward profile = %#v, %v", seeded, err)
	}
	if _, err := admin.BindStewardProfile(ctx, managementv1alpha1.BindStewardProfileRequest{
		ProfileID: historical.ProfileID, Version: historical.Version,
		SpaceIDs: []memoryv1alpha1.SpaceID{defaultMemorySpaceID},
	}); err != nil {
		t.Fatalf("bind historical Steward profile: %v", err)
	}
	document, err := newAppConfigStore(storeDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle:    agentbinding.HandleSteward,
		ProfileID: document.ModelProfiles.DefaultProfileID,
		Effort:    document.ModelProfiles.DefaultEffort,
	}); err != nil {
		t.Fatalf("persist explicit Steward binding: %v", err)
	}
	provider.Begin(memoryGoldenScenario{
		name:    "steward-upgrade-before",
		actions: []memoryGoldenAction{{name: memorytool.RememberToolName, arguments: `{"text":"` + memoryGoldenFact + `"}`}},
	})
	runMemoryGoldenSession(t, ctx, stack, "session-steward-upgrade-before")
	provider.AssertComplete(t)
	waitMemoryGoldenCondition(t, ctx, "one pending historical job", func() bool {
		inspection, inspectErr := admin.Inspect(ctx)
		return inspectErr == nil && inspection.Steward.PendingJobs == 1 && inspection.Steward.CompletedJobs == 0
	})
	if err := stack.Close(); err != nil {
		t.Fatalf("close seeded Host: %v", err)
	}

	// Reopen: the bridge must bind the current profile and drain the old Job.
	stack = newMemoryGoldenStack(t, provider, storeDir, workspace)
	waitMemoryGoldenCondition(t, ctx, "current profile drains the historical job", func() bool {
		configuration, configErr := stack.memoryRuntime.Management().GetStewardConfiguration(ctx)
		inspection, inspectErr := stack.memoryRuntime.Management().Inspect(ctx)
		return configErr == nil && inspectErr == nil && len(configuration.Bindings) == 1 &&
			configuration.Bindings[0].ProfileVersion == defaultMemoryStewardProfile.Version &&
			inspection.Steward.PendingJobs == 0 && inspection.Steward.CompletedJobs == 1
	})
	if !stack.memorySteward.active.Load() {
		t.Fatal("reopened Host did not enable the semantic Steward worker")
	}
	configuration, err := stack.memoryRuntime.Management().GetStewardConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryStewardUpgradeConfiguration(t, configuration, historical, release+" reopened")
	t.Logf("Memory Steward upgrade [%s]: profiles=%d bindings=%#v", release, len(configuration.Profiles), configuration.Bindings)

	// A new Remember is organized by the current profile.
	provider.Begin(memoryGoldenScenario{
		name:    "steward-upgrade-after",
		actions: []memoryGoldenAction{{name: memorytool.RememberToolName, arguments: `{"text":"the post-upgrade review language is Chinese"}`}},
	})
	runMemoryGoldenSession(t, ctx, stack, "session-steward-upgrade-after")
	provider.AssertComplete(t)
	waitMemoryGoldenCondition(t, ctx, "post-upgrade job organized", func() bool {
		inspection, inspectErr := stack.memoryRuntime.Management().Inspect(ctx)
		return inspectErr == nil && inspection.Steward.PendingJobs == 0 &&
			inspection.Steward.CompletedJobs == 2 && inspection.Steward.ActiveRecords == 2
	})
	if err := stack.Close(); err != nil {
		t.Fatalf("close upgraded Host: %v", err)
	}

	// A third start must be idempotent: the same two profiles, the current
	// binding, and no error recreating the current profile.
	stack = newMemoryGoldenStack(t, provider, storeDir, workspace)
	configuration, err = stack.memoryRuntime.Management().GetStewardConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryStewardUpgradeConfiguration(t, configuration, historical, release+" restarted")
	repeated, err := stack.memoryRuntime.Management().PutStewardProfile(ctx, managementv1alpha1.PutStewardProfileRequest{Profile: defaultMemoryStewardProfile})
	if err != nil || repeated.Created {
		t.Fatalf("current profile was recreated on restart = %#v, %v", repeated, err)
	}
}

func loadHistoricalStewardProfile(t *testing.T, release string) stewardv1alpha1.ProfileSpec {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join("testdata", "memory", "steward_profiles", release+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Release string                      `json:"release"`
		Profile stewardv1alpha1.ProfileSpec `json:"profile"`
	}
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Release != release {
		t.Fatalf("historical Steward fixture release = %q, want %q", fixture.Release, release)
	}
	if err := fixture.Profile.Validate(); err != nil {
		t.Fatalf("historical Steward fixture %s is invalid: %v", release, err)
	}
	return fixture.Profile
}

// assertMemoryStewardUpgradeConfiguration requires the current profile to be
// bound to the default Space and exactly the historical and current immutable
// profiles to be stored.
func assertMemoryStewardUpgradeConfiguration(t *testing.T, configuration managementv1alpha1.StewardConfiguration, historical stewardv1alpha1.ProfileSpec, stage string) {
	t.Helper()
	if len(configuration.Bindings) != 1 {
		t.Fatalf("%s Steward bindings = %#v, want the current one", stage, configuration.Bindings)
	}
	binding := configuration.Bindings[0]
	if binding.ProfileID != defaultMemoryStewardProfile.ProfileID || binding.SpaceID != memoryv1alpha1.SpaceID(defaultMemorySpaceID) ||
		binding.ProfileVersion != defaultMemoryStewardProfile.Version {
		t.Fatalf("%s Steward binding = %#v, want current profile on the default Space", stage, binding)
	}
	stored := make(map[uint64]stewardv1alpha1.Profile, len(configuration.Profiles))
	for _, profile := range configuration.Profiles {
		stored[profile.Version] = profile
	}
	if len(configuration.Profiles) != 2 {
		t.Fatalf("%s Steward profiles = %#v, want exactly historical and current", stage, configuration.Profiles)
	}
	if profile, ok := stored[historical.Version]; !ok || profile.ProfileSpec != historical {
		t.Fatalf("%s historical profile %d was not retained: %#v", stage, historical.Version, profile)
	}
	if profile, ok := stored[defaultMemoryStewardProfile.Version]; !ok || profile.ProfileSpec != defaultMemoryStewardProfile {
		t.Fatalf("%s current profile %d was not retained: %#v", stage, defaultMemoryStewardProfile.Version, profile)
	}
}
