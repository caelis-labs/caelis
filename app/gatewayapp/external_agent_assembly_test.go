package gatewayapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/plugin"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestExternalAgentAssemblyDoesNotOwnModelDefaults(t *testing.T) {
	t.Parallel()

	store := newAppConfigStore(t.TempDir())
	if err := store.Save(AppConfig{ExternalAgents: controlagents.Configuration{
		Connections: []controlagents.Connection{{
			ID:       "claude",
			Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindPackageExec, Command: "npx", Args: []string{"-y", "claude-agent-acp"}},
		}},
		Agents: []controlagents.Agent{{ID: "claude", Name: "Claude", ConnectionID: "claude"}},
	}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	stack := &Stack{runtimeComposition: runtimeComposition{store: store}}
	resolved, err := stack.withExternalACPAgents(assembly.ResolvedAssembly{}, stackRuntimeConfig{})
	if err != nil {
		t.Fatalf("withExternalACPAgents() error = %v", err)
	}
	if got, want := len(resolved.Agents), 1; got != want {
		t.Fatalf("len(Agents) = %d, want %d", got, want)
	}
	agent := resolved.Agents[0]
	if agent.Name != "claude" || agent.Command != "npx" || agent.SessionOptions.ModelID != "" || len(agent.SessionOptions.ConfigValues) != 0 {
		t.Fatalf("materialized Agent = %#v", agent)
	}
}

func TestExternalAgentAssemblyRejectsParallelLegacyTargetName(t *testing.T) {
	t.Parallel()

	store := newAppConfigStore(t.TempDir())
	if err := store.Save(AppConfig{ExternalAgents: controlagents.Configuration{
		Connections: []controlagents.Connection{{ID: "claude", Launcher: controlagents.Launcher{Command: "claude-acp"}}},
		Agents:      []controlagents.Agent{{ID: "claude", ConnectionID: "claude"}},
	}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	stack := &Stack{runtimeComposition: runtimeComposition{store: store}}
	_, err := stack.withExternalACPAgents(assembly.ResolvedAssembly{Agents: []assembly.AgentConfig{{Name: "claude", Command: "legacy-claude"}}}, stackRuntimeConfig{})
	if err == nil {
		t.Fatal("withExternalACPAgents() error = nil, want duplicate truth rejection")
	}
}

func TestPluginAgentCollisionFailsClosed(t *testing.T) {
	stack := &Stack{}
	_, err := stack.withPluginACPAgents(assembly.ResolvedAssembly{Agents: []assembly.AgentConfig{{
		Name: "opus", Command: "existing-opus",
	}}}, []plugin.AgentRegistration{{
		PluginID: "duplicate-plugin",
		Agent:    plugin.AgentContribution{Name: "opus", Command: "plugin-opus"},
	}})
	if err == nil || !strings.Contains(err.Error(), "conflicts with an existing Agent") {
		t.Fatalf("withPluginACPAgents() error = %v, want explicit collision", err)
	}
}

func TestCustomDirectRoleCollisionFailsClosedAndRollsBack(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{Agents: []assembly.AgentConfig{{
		Name: "research", Command: "existing-research",
	}}})
	profile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "research-model",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	revision, err := stack.ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.AgentCommands().CreateAgentRole(context.Background(), appserver.Principal{ID: stack.UserID}, appserver.CreateAgentRoleRequest{
		WriteBase: appserver.WriteBase{OperationID: "colliding-agent-role", ExpectedRevision: &revision},
		Role:      agentbinding.Role{Handle: "research", Description: "Investigate unfamiliar systems."},
		Binding:   agentbinding.Binding{ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort},
	})
	if err == nil || result.Outcome != appserver.OutcomeRejected {
		t.Fatalf("CreateAgentRole() = %#v, %v; want rejected collision", result, err)
	}
	doc, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if _, ok := agentbinding.LookupRole(doc.AgentBindings, "research"); ok {
		t.Fatalf("failed custom role mutation was persisted: %#v", doc.AgentBindings.Roles)
	}
	if doc.ConfigurationRevision != revision {
		t.Fatalf("rejected custom role advanced revision to %d, want %d", doc.ConfigurationRevision, revision)
	}
}

func TestExternalAgentAssemblyRejectsProductAndSystemNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"status", "reviewer", "guardian", "self", "breeze", "orbit", "zenith", "local", "main", "kernel", "sandbox", "worker(lina)", "bad name"} {
		t.Run(name, func(t *testing.T) {
			store := newAppConfigStore(t.TempDir())
			err := store.Save(AppConfig{ExternalAgents: controlagents.Configuration{
				Connections: []controlagents.Connection{{ID: "claude", Launcher: controlagents.Launcher{Command: "claude-acp"}}},
				Agents:      []controlagents.Agent{{ID: name, ConnectionID: "claude"}},
			}})
			if err == nil {
				t.Fatalf("Save(%q) error = nil, want external Agent validation", name)
			}
		})
	}
}

func TestProviderProfileBindingMaterializesFixedDirectHandle(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	stack.mu.Lock()
	runtimeCfg := stack.runtime
	runtimeCfg.SystemPrompt = "shared base prompt"
	stack.runtime = runtimeCfg
	stack.mu.Unlock()

	profile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "deepseek-v4-pro",
		ReasoningEffort: "xhigh", DefaultReasoningEffort: "high", ReasoningLevels: []string{"high", "xhigh"},
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.ExternalAgents.Agents) != 0 {
		t.Fatalf("provider connect created synthetic Agents: %#v", doc.ExternalAgents.Agents)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleBreeze, ProfileID: profile.ID, Effort: "xhigh",
	}); err != nil {
		t.Fatalf("BindAgentBinding() error = %v", err)
	}
	activated := activateFutureAssemblyStack(t, stack, "provider-profile-binding")
	var materialized assembly.AgentConfig
	for _, agent := range activated.runtime.Assembly.Agents {
		if agent.Name == string(agentbinding.HandleBreeze) {
			materialized = agent
			break
		}
	}
	if materialized.Command == "" {
		t.Fatalf("future Session assembly does not contain Breeze profile Agent: %#v", activated.runtime.Assembly.Agents)
	}
	if got := materialized.SessionOptions.ModelID; got != profile.Backend.Provider.ModelConfigID {
		t.Fatalf("materialized model session option = %q, want %q", got, profile.Backend.Provider.ModelConfigID)
	}
	if got := materialized.SessionOptions.ConfigValues[acpConfigModeID]; got != "manual" {
		t.Fatalf("materialized mode session option = %q, want manual", got)
	}
	if got := materialized.SessionOptions.ConfigValues[acpConfigReasoningID]; got != "xhigh" || materialized.SessionOptions.ReasoningEffortConfigID != acpConfigReasoningID {
		t.Fatalf("materialized reasoning session options = %#v, want xhigh", materialized.SessionOptions)
	}
	joined := strings.Join(materialized.Args, " ")
	for _, forbidden := range []string{"-model-profile", "-system-prompt", "-reasoning-effort", "-context-window", "-policy-profile", "-approval-mode", "--embedded"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("materialized managed child args = %#v, contain Host-construction option %q", materialized.Args, forbidden)
		}
	}
	if strings.TrimSpace(materialized.Env[systemSceneEnvKey]) != "" {
		t.Fatalf("model Agent inherited system-scene marker: %#v", materialized.Env)
	}
}

func TestReviewerACPBindingMaterializesHiddenReviewScene(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc.ExternalAgents = controlagents.Configuration{
		Connections: []controlagents.Connection{{
			ID: "claude", Name: "Claude", Launcher: controlagents.Launcher{
				Kind: controlagents.LaunchKindExecutable, Command: "claude-acp", Args: []string{"--stdio"},
			},
		}},
		Agents: []controlagents.Agent{{ID: "claude", Name: "Claude", ConnectionID: "claude"}},
	}
	doc.ModelProfiles.Profiles = append(doc.ModelProfiles.Profiles, modelprofile.ModelProfile{
		ID: "acp:claude:opus", DisplayName: "Claude Opus",
		Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{
			AgentID: "claude", RemoteModelID: "Opus-V4", SessionDefaults: map[string]string{"mode": "code"},
		}},
		Effort: modelprofile.EffortCapability{
			DefaultEffort: "high", ACPConfigID: "thought_level",
			Choices: []modelprofile.EffortChoice{{Canonical: "high", WireValue: "high"}, {Canonical: "xhigh", WireValue: "very-high"}},
		},
	})
	if err := stack.store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	stack.invalidatePlacementSnapshot()
	if _, err := stack.testAgentBindings().BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleReviewer, ProfileID: "acp:claude:opus", Effort: "xhigh",
	}); err != nil {
		t.Fatalf("BindAgentBinding(Reviewer ACP) error = %v", err)
	}
	activated := activateFutureAssemblyStack(t, stack, "reviewer-acp-binding")
	agents := append([]assembly.AgentConfig(nil), activated.runtime.Assembly.Agents...)
	for _, agent := range agents {
		if agent.Name != string(agentbinding.HandleReviewer) {
			continue
		}
		if agent.Command != "claude-acp" || strings.TrimSpace(agent.Env[systemSceneEnvKey]) != string(agentbinding.HandleReviewer) {
			t.Fatalf("Reviewer ACP scene = %#v", agent)
		}
		if agent.SessionOptions.ModelID != "Opus-V4" || agent.SessionOptions.ReasoningEffortConfigID != "thought_level" {
			t.Fatalf("Reviewer ACP session options = %#v", agent.SessionOptions)
		}
		if agent.SessionOptions.ConfigValues["mode"] != "code" || agent.SessionOptions.ConfigValues["thought_level"] != "very-high" {
			t.Fatalf("Reviewer ACP config values = %#v", agent.SessionOptions.ConfigValues)
		}
		return
	}
	t.Fatalf("runtime assembly = %#v, want ACP-backed hidden Reviewer", agents)
}

func TestSystemAgentBindingsApplySelectedModelAndEffort(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	profile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "reviewer-specialist",
		ReasoningMode: "effort", ReasoningEffort: "high", ReasoningLevels: []string{"high", "xhigh"},
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleReviewer, ProfileID: profile.ID, Effort: "xhigh",
	}); err != nil {
		t.Fatalf("BindAgentBinding(Reviewer) error = %v", err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleGuardian, ProfileID: profile.ID, Effort: "xhigh",
	}); err != nil {
		t.Fatalf("BindAgentBinding(Guardian) error = %v", err)
	}
	activated := activateFutureAssemblyStack(t, stack, "system-agent-bindings")
	guardian, bound, err := activated.resolveSystemAgentModel(context.Background(), agentbinding.HandleGuardian, 0)
	if err != nil || !bound || guardian.Model == nil || guardian.ReasoningEffort != "xhigh" {
		t.Fatalf("resolveSystemAgentModel(Guardian) = (%#v, %v, %v), want xhigh binding", guardian, bound, err)
	}

	agents := append([]assembly.AgentConfig(nil), activated.runtime.Assembly.Agents...)
	for _, agent := range agents {
		if agent.Name != string(agentbinding.HandleReviewer) {
			continue
		}
		if got := agent.SessionOptions.ModelID; got != profile.Backend.Provider.ModelConfigID {
			t.Fatalf("Reviewer model session option = %q, want %q", got, profile.Backend.Provider.ModelConfigID)
		}
		if got := agent.SessionOptions.ConfigValues[acpConfigReasoningID]; got != "xhigh" || agent.SessionOptions.ReasoningEffortConfigID != acpConfigReasoningID {
			t.Fatalf("Reviewer reasoning session options = %#v, want xhigh", agent.SessionOptions)
		}
		if got := agent.SessionOptions.ConfigValues[acpConfigModeID]; got != "manual" {
			t.Fatalf("Reviewer mode session option = %q, want manual", got)
		}
		joined := strings.Join(agent.Args, " ")
		for _, forbidden := range []string{"-model-profile", "-reasoning-effort", "-approval-mode", "--embedded"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("Reviewer managed child args = %#v, contain Host-construction option %q", agent.Args, forbidden)
			}
		}
		return
	}
	t.Fatalf("runtime assembly = %#v, want fixed Reviewer", agents)
}

func activateFutureAssemblyStack(t *testing.T, stack *Stack, sessionID string) *sessionRuntimeInstance {
	t.Helper()
	started, err := startGatewayAppTestSession(context.Background(), stack, sessionID)
	if err != nil {
		t.Fatalf("start future Session: %v", err)
	}
	activated, _, err := stack.sessionRuntimes.activateSession(context.Background(), started.SessionID)
	if err != nil {
		t.Fatalf("activate future Session: %v", err)
	}
	return activated.stack
}
