package gatewayapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/ports/plugin"
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
	stack := &Stack{store: store}
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
	stack := &Stack{store: store}
	_, err := stack.withExternalACPAgents(assembly.ResolvedAssembly{Agents: []assembly.AgentConfig{{Name: "claude", Command: "legacy-claude"}}}, stackRuntimeConfig{})
	if err == nil {
		t.Fatal("withExternalACPAgents() error = nil, want duplicate truth rejection")
	}
}

func TestPluginAgentCollisionFailsClosed(t *testing.T) {
	stack := &Stack{}
	_, err := stack.withPluginACPAgents(assembly.ResolvedAssembly{Agents: []assembly.AgentConfig{{
		Name: "opus", Command: "existing-opus",
	}}}, []pluginAgentContribution{{
		PluginID: "duplicate-plugin",
		Agent:    plugin.AgentContribution{Name: "opus", Command: "plugin-opus"},
	}})
	if err == nil || !strings.Contains(err.Error(), "conflicts with an existing Agent") {
		t.Fatalf("withPluginACPAgents() error = %v, want explicit collision", err)
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

	profile, err := stack.Connect(ModelConfig{
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
	if _, err := stack.AgentBindings().BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleBreeze, ProfileID: profile.ID, Effort: "xhigh",
	}); err != nil {
		t.Fatalf("BindAgentBinding() error = %v", err)
	}
	var materialized assembly.AgentConfig
	for _, agent := range stack.runtime.Assembly.Agents {
		if agent.Name == string(agentbinding.HandleBreeze) {
			materialized = agent
			break
		}
	}
	if materialized.Command == "" {
		t.Fatalf("runtime assembly does not contain Breeze profile Agent: %#v", stack.runtime.Assembly.Agents)
	}
	if got, _ := argValue(materialized.Args, "-model"); got != "deepseek-v4-pro" {
		t.Fatalf("materialized -model = %q, want deepseek-v4-pro; args=%#v", got, materialized.Args)
	}
	if got, _ := argValue(materialized.Args, "-system-prompt"); got != "shared base prompt" {
		t.Fatalf("materialized system prompt = %q, want shared base prompt", got)
	}
	if got, _ := argValue(materialized.Args, "-reasoning-effort"); got != "xhigh" {
		t.Fatalf("materialized reasoning effort = %q, want xhigh", got)
	}
	if got, _ := argValue(materialized.Args, "-reasoning-levels"); got != "high,xhigh" {
		t.Fatalf("materialized reasoning levels = %q, want high,xhigh", got)
	}
	if strings.TrimSpace(materialized.Env[systemSceneEnvKey]) != "" {
		t.Fatalf("model Agent inherited system-scene marker: %#v", materialized.Env)
	}
}

func TestSystemAgentBindingsApplySelectedModelAndEffort(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	profile, err := stack.Connect(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "reviewer-specialist",
		ReasoningMode: "effort", ReasoningEffort: "high", ReasoningLevels: []string{"high", "xhigh"},
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if _, err := stack.AgentBindings().BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleReviewer, ProfileID: profile.ID, Effort: "xhigh",
	}); err != nil {
		t.Fatalf("BindAgentBinding(Reviewer) error = %v", err)
	}
	if _, err := stack.AgentBindings().BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleGuardian, ProfileID: profile.ID, Effort: "xhigh",
	}); err != nil {
		t.Fatalf("BindAgentBinding(Guardian) error = %v", err)
	}
	guardian, bound, err := stack.resolveSystemAgentModel(context.Background(), agentbinding.HandleGuardian, 0)
	if err != nil || !bound || guardian.Model == nil || guardian.ReasoningEffort != "xhigh" {
		t.Fatalf("resolveSystemAgentModel(Guardian) = (%#v, %v, %v), want xhigh binding", guardian, bound, err)
	}

	stack.mu.RLock()
	agents := append([]assembly.AgentConfig(nil), stack.runtime.Assembly.Agents...)
	stack.mu.RUnlock()
	for _, agent := range agents {
		if agent.Name != ReviewerAgentID {
			continue
		}
		if got, _ := argValue(agent.Args, "-model"); got != "reviewer-specialist" {
			t.Fatalf("Reviewer -model = %q, want reviewer-specialist; args=%#v", got, agent.Args)
		}
		if got, _ := argValue(agent.Args, "-reasoning-effort"); got != "xhigh" {
			t.Fatalf("Reviewer -reasoning-effort = %q, want xhigh; args=%#v", got, agent.Args)
		}
		return
	}
	t.Fatalf("runtime assembly = %#v, want fixed Reviewer", agents)
}
