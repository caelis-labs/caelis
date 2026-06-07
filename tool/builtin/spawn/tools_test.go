package spawn

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/tool"
)

type fakeToolContext struct {
	context.Context
}

func (c fakeToolContext) SessionRef() string             { return "session-1" }
func (c fakeToolContext) InvocationID() string           { return "inv-1" }
func (c fakeToolContext) AgentName() string              { return "agent-1" }
func (c fakeToolContext) FileSystem() sandbox.FileSystem { return nil }

type fakeDelegator struct {
	req agent.SpawnRequest
}

func (d *fakeDelegator) Spawn(_ tool.Context, req agent.SpawnRequest) (agent.SpawnResult, error) {
	d.req = req
	return agent.SpawnResult{HandleID: "child-1", FinalMessage: "child done"}, nil
}

func TestSpawnToolRequiresPrompt(t *testing.T) {
	result, err := New(&fakeDelegator{}).Run(testCtx(), tool.Call{Args: map[string]any{}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.IsError || result.Output != "prompt is required" {
		t.Fatalf("result = %#v, want required prompt error", result)
	}
}

func TestSpawnToolRequiresDelegator(t *testing.T) {
	result, err := All()[0].Run(testCtx(), tool.Call{Args: map[string]any{"prompt": "do work"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.IsError || result.Output != "SPAWN: no delegator configured" {
		t.Fatalf("result = %#v, want no-delegator error", result)
	}
}

func TestSpawnToolDelegatesWithInvocationID(t *testing.T) {
	delegator := &fakeDelegator{}
	result, err := New(delegator).Run(testCtx(), tool.Call{Args: map[string]any{
		"agent":  "reviewer",
		"prompt": "review this change",
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "child done" || result.Metadata["handle_id"] != "child-1" {
		t.Fatalf("result = %#v, want child output and handle", result)
	}
	if delegator.req.AgentName != "reviewer" || delegator.req.Prompt != "review this change" || delegator.req.RunID != "inv-1" {
		t.Fatalf("delegator req = %#v", delegator.req)
	}
}

func testCtx() tool.Context {
	return fakeToolContext{Context: context.Background()}
}
