package controlprompt

import (
	"context"
	"testing"
)

func TestReviewAndParticipantHandleRouting(t *testing.T) {
	for _, command := range []string{"/review", "/review recheck", "/lina recheck", "/reviewer(lina) recheck", "/orbit recheck", "/reviewer(orbit) recheck", "/unknown recheck"} {
		t.Run(command, func(t *testing.T) {
			svc := &fakeService{turn: &fakeTurn{id: "turn"}, agentStatus: AgentStatusSnapshot{Participants: []AgentParticipantSnapshot{
				{Label: "@lina", Kind: "subagent", Role: "sidecar", Source: "slash_review"},
				{Label: "@orbit", Kind: "subagent", Role: "sidecar", Source: "slash_review"},
				{Label: "@review", Kind: "subagent", Role: "sidecar", Source: "slash_profile_orbit"},
			}}}
			result, err := New(RouterConfig{Service: svc}).Route(t.Context(), Request{Submission: Submission{Text: command}})
			if err != nil {
				t.Fatal(err)
			}
			switch command {
			case "/review", "/review recheck":
				if result.ParticipantTask == nil || result.ParticipantTask.TaskID != "review-task" || result.Turn != nil || !result.RefreshCommands || svc.continuedHandle != "" {
					t.Fatalf("review did not create a new background task: %#v", result)
				}
			case "/orbit recheck":
				if svc.startedAgent != "orbit" || svc.continuedHandle != "" {
					t.Fatalf("configured role lost precedence: start=%q continue=%q", svc.startedAgent, svc.continuedHandle)
				}
			case "/unknown recheck":
				if svc.submitted.Text != command || svc.continuedHandle != "" {
					t.Fatal("unknown handle routed to a child")
				}
			default:
				name, _, _, _ := ParseSlash(command)
				if svc.continuedHandle != name || svc.continuedPrompt != "recheck" || svc.startedAgent != "" || svc.submitted.Text != "" {
					t.Fatalf("wrong followup: continue=%q start=%q submit=%q", svc.continuedHandle, svc.startedAgent, svc.submitted.Text)
				}
			}
		})
	}
}

func TestBareParticipantHandlesRespectAgentFilter(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		svc := &fakeService{turn: &fakeTurn{id: "turn"}, agentStatus: AgentStatusSnapshot{Participants: []AgentParticipantSnapshot{
			{Label: "@lina", Kind: "subagent", Role: "sidecar", Source: "slash_review"},
		}}}
		router := New(RouterConfig{Service: svc, DynamicCommandAllowed: func(_ context.Context, agent string) bool { return allowed && agent == "reviewer" }})
		if _, err := router.Route(t.Context(), Request{Submission: Submission{Text: "/lina recheck"}}); err != nil {
			t.Fatal(err)
		}
		if (svc.continuedHandle == "lina") != allowed {
			t.Fatalf("agent filter ignored: allowed=%v continued=%q", allowed, svc.continuedHandle)
		}
	}
}
