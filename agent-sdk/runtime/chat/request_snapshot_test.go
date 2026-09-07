package chat

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestFactoryRebuildsRequestsFromNewConfigurationWithoutChangingActiveSnapshot(t *testing.T) {
	llm := &recordingModel{}
	stream := false
	spec := agent.AgentSpec{Name: "main", Model: llm, Metadata: map[string]any{"system_prompt": "original instructions", "reasoning_effort": "low"}, Request: agent.ModelRequestOptions{Stream: &stream, ServiceTier: model.ServiceTierPriority}}
	factory := Factory{}
	active, err := factory.NewAgent(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	// Configuration is mutable outside the assembled Agent. A later assembly
	// samples it; the already assembled Agent must retain its detached snapshot.
	stream = true
	spec.Metadata["system_prompt"] = "updated instructions"
	spec.Metadata["reasoning_effort"] = "high"
	spec.Request.ServiceTier = ""
	run := func(a agent.Agent) model.Request {
		t.Helper()
		for _, err := range a.Run(agent.NewContext(agent.ContextSpec{Context: context.Background()})) {
			if err != nil {
				t.Fatal(err)
			}
		}
		return llm.last
	}
	old := run(active)
	if old.Stream || old.ServiceTier != model.ServiceTierPriority || old.Reasoning.Effort != "low" || len(old.Instructions) != 1 || old.Instructions[0].Text.Text != "original instructions" {
		t.Fatalf("active request changed: %#v", old)
	}
	next, err := factory.NewAgent(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	updated := run(next)
	if !updated.Stream || updated.ServiceTier != "" || updated.Reasoning.Effort != "high" || len(updated.Instructions) != 1 || updated.Instructions[0].Text.Text != "updated instructions" {
		t.Fatalf("new request retained old configuration: %#v", updated)
	}
}
