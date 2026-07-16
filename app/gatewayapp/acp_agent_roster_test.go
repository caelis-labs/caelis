package gatewayapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlsystemagent "github.com/caelis-labs/caelis/control/systemagent"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/ports/plugin"
)

func TestAgentRosterMaterializesOneSpawnAndHandoffTargetWithDefaults(t *testing.T) {
	t.Parallel()

	store := newAppConfigStore(t.TempDir())
	if err := store.Save(AppConfig{AgentRoster: controlagents.Configuration{
		Connections: []controlagents.Connection{{
			ID:       "claude",
			Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindPackageExec, Command: "npx", Args: []string{"-y", "claude-agent-acp"}},
		}},
		Agents: []controlagents.Agent{{
			ID:       "opus",
			Name:     "Opus",
			Backing:  controlagents.AgentBacking{ConnectionID: "claude"},
			Defaults: controlagents.SessionOptions{ModelID: "claude-opus-4-8", ConfigValues: map[string]string{"effort": "max"}},
		}},
	}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	stack := &Stack{store: store}
	resolved, err := stack.withAgentRosterACPAgents(assembly.ResolvedAssembly{}, stackRuntimeConfig{})
	if err != nil {
		t.Fatalf("withAgentRosterACPAgents() error = %v", err)
	}
	if got, want := len(resolved.Agents), 1; got != want {
		t.Fatalf("len(Agents) = %d, want %d", got, want)
	}
	agent := resolved.Agents[0]
	if agent.Name != "opus" || agent.Command != "npx" || agent.SessionOptions.ModelID != "claude-opus-4-8" || agent.SessionOptions.ConfigValues["effort"] != "max" {
		t.Fatalf("materialized Agent = %#v", agent)
	}
}

func TestAgentRosterRejectsParallelLegacyTargetName(t *testing.T) {
	t.Parallel()

	store := newAppConfigStore(t.TempDir())
	if err := store.Save(AppConfig{AgentRoster: controlagents.Configuration{
		Connections: []controlagents.Connection{{ID: "claude", Launcher: controlagents.Launcher{Command: "claude-acp"}}},
		Agents:      []controlagents.Agent{{ID: "opus", Backing: controlagents.AgentBacking{ConnectionID: "claude"}}},
	}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	stack := &Stack{store: store}
	_, err := stack.withAgentRosterACPAgents(assembly.ResolvedAssembly{Agents: []assembly.AgentConfig{{Name: "opus", Command: "legacy-opus"}}}, stackRuntimeConfig{})
	if err == nil {
		t.Fatal("withAgentRosterACPAgents() error = nil, want duplicate truth rejection")
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

func TestAgentRosterRejectsProductAndSystemNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"status", "reviewer", "guardian", "self", "breeze", "orbit", "zenith", "local", "main", "kernel", "sandbox", "worker(lina)", "bad name"} {
		t.Run(name, func(t *testing.T) {
			store := newAppConfigStore(t.TempDir())
			err := store.Save(AppConfig{AgentRoster: controlagents.Configuration{
				Connections: []controlagents.Connection{{ID: "claude", Launcher: controlagents.Launcher{Command: "claude-acp"}}},
				Agents:      []controlagents.Agent{{ID: name, Backing: controlagents.AgentBacking{ConnectionID: "claude"}}},
			}})
			if err == nil {
				t.Fatalf("Save(%q) error = nil, want product roster validation", name)
			}
		})
	}
}

func TestConnectedModelAgentMaterializesGenericSelfRuntimeWithoutProfilePrompt(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	stack.mu.Lock()
	runtimeCfg := stack.runtime
	runtimeCfg.SystemPrompt = "shared base prompt"
	stack.runtime = runtimeCfg
	stack.mu.Unlock()

	modelID, err := stack.Connect(ModelConfig{
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
	var rosterAgent controlagents.Agent
	for _, agent := range controlagents.ListAgents(doc.AgentRoster) {
		if agent.Backing.ModelAlias == modelID {
			rosterAgent = agent
			break
		}
	}
	if rosterAgent.ID == "" || rosterAgent.Backing.ConnectionID != "" {
		t.Fatalf("model-backed roster Agent = %#v", rosterAgent)
	}
	var materialized assembly.AgentConfig
	for _, agent := range stack.runtime.Assembly.Agents {
		if agent.Name == rosterAgent.ID {
			materialized = agent
			break
		}
	}
	if materialized.Command == "" {
		t.Fatalf("runtime assembly does not contain model Agent %q: %#v", rosterAgent.ID, stack.runtime.Assembly.Agents)
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
	modelID, err := stack.Connect(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "reviewer-specialist",
		ReasoningMode: "effort", ReasoningEffort: "high", ReasoningLevels: []string{"high", "xhigh"},
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	agentID := ""
	for _, agent := range controlagents.ListAgents(doc.AgentRoster) {
		if agent.Backing.ModelAlias == modelID {
			agentID = agent.ID
			break
		}
	}
	if agentID == "" {
		t.Fatalf("AgentRoster = %#v, want model Agent for %q", doc.AgentRoster, modelID)
	}
	if _, err := stack.SystemAgents().BindSystemAgent(context.Background(), controlsystemagent.BindRequest{
		ID: controlsystemagent.Reviewer, AgentID: agentID, ReasoningEffort: "xhigh",
	}); err != nil {
		t.Fatalf("BindSystemAgent(Reviewer) error = %v", err)
	}
	if _, err := stack.SystemAgents().BindSystemAgent(context.Background(), controlsystemagent.BindRequest{
		ID: controlsystemagent.Guardian, AgentID: agentID, ReasoningEffort: "xhigh",
	}); err != nil {
		t.Fatalf("BindSystemAgent(Guardian) error = %v", err)
	}
	guardian, bound, err := stack.resolveSystemAgentModel(context.Background(), controlsystemagent.Guardian, 0)
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
