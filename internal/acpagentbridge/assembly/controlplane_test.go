package assembly

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/assembly"
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

	if err := controlPlane.Updater.UpdateAgents([]assembly.AgentConfig{
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
}
