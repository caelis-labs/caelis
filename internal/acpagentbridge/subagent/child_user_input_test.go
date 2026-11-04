package subagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestUserChildInputActiveAndIdleKeepHumanProvenance(t *testing.T) {
	for _, mode := range []string{"active", "idle"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			// Keep the active Prompt open until its steering receipt settles.
			// A terminal can legitimately cancel an unobserved input receipt.
			release := filepath.Join(t.TempDir(), "terminal-release")
			runner := newChildInputTestRunner(t, mode, map[string]string{
				"CAELIS_ACP_CHILD_INPUT_TERMINAL_RELEASE": release,
			}, controlagents.Authentication{})
			defer func() {
				if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
					t.Error(err)
				}
				_ = runner.Quiesce(ctx)
			}()
			events := make(chan childInputTestEvent, 32)
			spawn := childInputSpawnContext(t, "task-user-"+mode, events)
			anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial"})
			if err != nil {
				t.Fatal(err)
			}
			consumeSpawnInitialInput(t, ctx, events, spawn.ActivityID, "initial")
			if mode == "idle" {
				waitChildActivityTerminal(t, ctx, events)
			}
			run, err := runner.lookup(anchor)
			if err != nil {
				t.Fatal(err)
			}
			req := agent.ChildInputRequest{Target: run.slot.target, ActivityID: "user-followup", Source: session.ActorRef{Kind: session.ActorKindUser, ID: "owner", Name: "user"}, Input: "guide now", UserInput: true}
			parts := buildAgentCommunicationPrompt(req)
			var part struct {
				Text string `json:"text"`
			}
			if len(parts) == 1 {
				_ = json.Unmarshal(parts[0], &part)
			}
			body, quoted := unquoteUserPrompt(part.Text)
			if len(parts) != 1 || !quoted || body != "guide now" {
				t.Fatalf("human prompt lost its literal user body: %#v", parts)
			}
			_, err = submitChildInputTest(runner, events, ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for !found {
				select {
				case event := <-events:
					if event.Frame == nil || event.Frame.Event == nil {
						continue
					}
					canonical := event.Frame.Event
					if canonical.Text != "guide now" {
						continue
					}
					if canonical.Type != session.EventTypeUser || canonical.Actor.ID != "owner" || session.ProtocolAgentCommunicationOf(canonical) != nil {
						t.Fatalf("accepted human prompt=%#v", canonical)
					}
					found = true
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}
