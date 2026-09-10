package subagent

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestLoadedAgentCommunicationPromptRestoresCanonicalParentIdentity(t *testing.T) {
	t.Parallel()

	header := session.AgentCommunicationPromptFooter(session.ControllerExecutor(session.ControllerBinding{
		Kind: session.ControllerKindKernel, ControllerID: "sdk-kernel", AgentName: "local",
	}))
	for _, prompt := range []string{
		"continue from parent" + header,
		"[Internal agent message]\nSender: local\nKind: controller\nSender ID: sdk-kernel\nMessage:\ncontinue from parent",
	} {
		got, body, format := loadedAgentCommunicationPrompt(prompt)
		if format == loadedMailNone || got != session.ParentCommunicationActor() || body != "continue from parent" {
			t.Fatalf("loaded parent communication = (%#v, %q, %v), want canonical parent identity", got, body, format)
		}
	}
	if strings.Contains(header, "local") {
		t.Fatalf("parent prompt leaked local controller name: %q", header)
	}
}

func TestLoadedMailKeepsChunkedBodySourcesAndOmitsSetup(t *testing.T) {
	t.Parallel()
	collector := newHistoryCollector(&Runner{clock: time.Now}, delegation.Anchor{SessionID: "child-1", TaskID: "task-1"}, "helper")
	observe := func(kind, text string) { collector.observe(contentUpdate(t, kind, text)) }
	observe(client.UpdateUserMessage, "initial task\n\nFrom: parent")
	observe(client.UpdateUserMessage, collaboration.RenderPromptSlice(collaboration.PromptSlice{Handle: "helper", Role: "delegated"}))
	observe(client.UpdateAgentMessage, "initial result")
	observe(client.UpdateUserMessage, "review ")
	observe(client.UpdateUserMessage, "the change")
	observe(client.UpdateUserMessage, "\n\nFrom: reviewer")
	observe(client.UpdateUserMessage, "then validate\n\nMessage-ID: 00000000-0000-0000-0000-000000000001\n\nFrom: parent")
	events := collector.eventsSnapshot()
	if len(events) != 5 {
		t.Fatalf("loaded events = %#v, want task, answer, two body chunks and second mail", events)
	}
	for i, want := range []string{"initial task", "initial result", "review ", "the change", "then validate"} {
		if events[i].Text != want {
			t.Fatalf("event %d text = %q, want %q", i, events[i].Text, want)
		}
	}
	if events[2].Actor.Name != "reviewer" || events[3].Actor.Name != "reviewer" || events[4].Actor.Name != "parent" {
		t.Fatalf("loaded source attribution = %#v", events)
	}
	if events[2].Scope.TurnID != events[4].Scope.TurnID || events[0].Scope.TurnID == events[2].Scope.TurnID {
		t.Fatal("loaded mail changed Turn grouping")
	}
}

func TestLoadedLegacyMailPreservesEachSenderInBatch(t *testing.T) {
	t.Parallel()
	for _, inlineBody := range []bool{false, true} {
		name := "separate body"
		if inlineBody {
			name = "inline body"
		}
		t.Run(name, func(t *testing.T) {
			collector := newHistoryCollector(&Runner{clock: time.Now}, delegation.Anchor{SessionID: "child-1", TaskID: "task-1"}, "helper")
			observe := func(text string) { collector.observe(contentUpdate(t, client.UpdateUserMessage, text)) }
			messages := []struct {
				header string
				body   string
				actor  session.ActorRef
			}{
				{
					header: "[Internal agent message]\nSender: reviewer\nKind: participant\nRole: delegated\nSender ID: reviewer-1\nMessage:",
					body:   "reviewer body",
					actor:  session.ActorRef{Kind: session.ActorKindParticipant, ID: "reviewer-1", Role: "delegated", Name: "reviewer"},
				},
				{
					header: "[Internal agent message]\nSender: local\nKind: controller\nSender ID: sdk-kernel\nMessage:",
					body:   "parent body",
					actor:  session.ParentCommunicationActor(),
				},
			}
			for _, message := range messages {
				if inlineBody {
					observe(message.header + "\n" + message.body)
				} else {
					observe(message.header)
					observe(message.body)
				}
				observe(" continued")
			}
			events := collector.eventsSnapshot()
			if len(events) != 4 {
				t.Fatalf("loaded %d events, want four body chunks", len(events))
			}
			for i, event := range events {
				message := messages[i/2]
				wantText := message.body
				if i%2 != 0 {
					wantText = " continued"
				}
				if event.Text != wantText || event.Actor != message.actor {
					t.Fatalf("event %d = (%q, %#v), want (%q, %#v)", i, event.Text, event.Actor, wantText, message.actor)
				}
				if event.Scope.TurnID != events[0].Scope.TurnID {
					t.Fatal("legacy mail batch was split into multiple Turns")
				}
			}
		})
	}
}
