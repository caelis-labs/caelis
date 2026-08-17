package assembly

import (
	"testing"

	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestNewControlPlaneUpdateAgentsPreservesInstances(t *testing.T) {
	controlPlane, err := NewControlPlane(ControlPlaneConfig{
		Agents: []assembly.AgentConfig{{
			Name:    "helper",
			Command: "helper-acp",
		}},
	})
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	oldControllers := controlPlane.Controllers
	oldSubagents := controlPlane.Subagents

	if err := controlPlane.UpdateAgents([]assembly.AgentConfig{
		{Name: "helper", Command: "helper-acp"},
		{Name: "copilot", Command: "copilot", Args: []string{"--acp"}},
	}); err != nil {
		t.Fatalf("UpdateAgents() error = %v", err)
	}
	if controlPlane.Controllers != oldControllers {
		t.Fatal("UpdateAgents replaced controller backend")
	}
	if controlPlane.Subagents != oldSubagents {
		t.Fatal("UpdateAgents replaced subagent runner")
	}
	if err := controlPlane.UpdateAgents([]assembly.AgentConfig{{Name: "broken"}}); err == nil {
		t.Fatal("UpdateAgents() error = nil, want invalid registry replacement to fail")
	}
	if _, err := controlPlane.registry.Resolve("helper"); err != nil {
		t.Fatalf("failed UpdateAgents() replaced previous registry: %v", err)
	}
}

func TestControlPlaneUpdateAgentsRequiresRegistry(t *testing.T) {
	t.Parallel()

	var controlPlane *ControlPlane
	if err := controlPlane.UpdateAgents(nil); err == nil {
		t.Fatal("UpdateAgents() error = nil, want unavailable registry")
	}
}
