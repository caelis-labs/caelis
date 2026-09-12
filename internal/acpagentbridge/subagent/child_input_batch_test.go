package subagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

func TestIdleChildBatchStartsOnePromptAndProjectsEverySource(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "idle-no-steering")
	events := make(chan childInputTestEvent, 24)
	spawn := childInputSpawnContext(t, "task-idle-batch", events)
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	run.mu.RLock()
	steering := run.supportsSteering
	run.mu.RUnlock()
	if steering {
		t.Fatal("fixture unexpectedly supports steering")
	}
	inputs := []agent.AgentCommunicationInput{
		{Source: session.ParentCommunicationActor(), Input: "first\n\nMessage-ID: mail-1", DisplayInput: "first"},
		{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "sibling", Name: "sibling"}, Input: "second\n\nMessage-ID: mail-2", DisplayInput: "second"},
	}
	result, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{Target: run.slot.target, Messages: inputs})
	if err != nil || !result.StartedActivity {
		t.Fatalf("batch admission: %#v %v", result, err)
	}
	frames, terminal := waitChildActivityFramesUntilTerminalFor(t, ctx, events, result.ActivityID)
	var accepted int
	running := 0
	for _, frame := range frames {
		if isProducerRunningEvidence(frame) {
			running++
			continue
		}
		if communication := session.ProtocolAgentCommunicationOf(frame.Event); communication != nil {
			if accepted == 0 && running != 1 {
				t.Fatalf("accepted batch input before producer running evidence: %#v", frames)
			}
			if accepted >= len(inputs) || communication.Text != inputs[accepted].DisplayInput || frame.Event.Actor.ID != inputs[accepted].Source.ID {
				t.Fatalf("source/order mismatch: %#v", frame.Event)
			}
			accepted++
		} else if frame.Event == nil || frame.Event.Text != "prompt output 2" {
			t.Fatalf("unexpected prompt: %#v", frame.Event)
		}
	}
	if running != 1 || accepted != 2 || len(frames) != 4 || terminal.Result.State != delegation.StateCompleted {
		t.Fatalf("running %d frames %d accepted %d terminal %#v", running, len(frames), accepted, terminal.Result)
	}
	encoded, _ := json.Marshal(buildAgentCommunicationPrompt(agent.ChildInputRequest{Messages: inputs}))
	text := string(encoded)
	if strings.Count(text, "From: parent") != 1 || strings.Count(text, "From: sibling") != 1 || strings.Index(text, "mail-1") > strings.Index(text, "mail-2") {
		t.Fatalf("prompt lost attributed order: %s", text)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestActiveChildBatchSteersOnceAndProjectsEverySource(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "active")
	events := make(chan childInputTestEvent, 16)
	spawn := childInputSpawnContext(t, "task-active-batch", events)
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	consumeSpawnInitialInput(t, ctx, events, spawn.ActivityID, "initial")
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []agent.AgentCommunicationInput{
		{Source: session.ParentCommunicationActor(), Input: "first report", DisplayInput: "First report"},
		{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "sibling", Name: "sibling"}, Input: "second report", DisplayInput: "Second report"},
	}
	result, err := runner.SubmitChildInputBatch(ctx, agent.ChildInputRequest{Target: run.slot.target, Messages: inputs})
	if err != nil || result.StartedActivity || result.ActivityID != spawn.ActivityID {
		t.Fatalf("active batch = %#v, %v", result, err)
	}
	frames, terminal := waitChildActivityFramesUntilTerminalFor(t, ctx, events, result.ActivityID)
	accepted := 0
	for _, frame := range frames {
		if communication := session.ProtocolAgentCommunicationOf(frame.Event); communication != nil {
			if accepted >= len(inputs) || communication.Text != inputs[accepted].DisplayInput || frame.Event.Actor.ID != inputs[accepted].Source.ID {
				t.Fatalf("batch projection changed: %#v", frame.Event)
			}
			accepted++
		} else if frame.Event.Text != "steered output" {
			t.Fatalf("unexpected activity: %#v", frame.Event)
		}
	}
	if accepted != 2 || len(frames) != 3 || terminal.Result.State != delegation.StateCompleted {
		t.Fatalf("frames=%d accepted=%d terminal=%#v", len(frames), accepted, terminal.Result)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}
