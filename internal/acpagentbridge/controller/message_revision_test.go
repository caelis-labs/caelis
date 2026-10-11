package controller

import (
	"context"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	sdkcontroller "github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
)

func TestDraftControllerPreviousTurnRevisionKeepsCanonicalResult(t *testing.T) {
	for _, test := range []struct {
		kind   string
		before bool
		opaque bool
	}{{"agent_message", true, false}, {"agent_message", false, false}, {"agent_thought", true, false}, {"agent_thought", false, false}, {"agent_message", true, true}, {"agent_message", false, true}, {"agent_thought", true, true}, {"agent_thought", false, true}} {
		name := "after current answer"
		if test.before {
			name = "before current answer"
		}
		if test.opaque {
			name = "opaque/" + name
		}
		t.Run(test.kind+"/"+name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			firstID, secondID, oldThoughtID, currentThoughtID := "answer-one", "answer-two", "answer-two", "current-thought"
			if test.opaque {
				firstID, secondID, oldThoughtID, currentThoughtID = " answer ", "answer", " thought ", "thought"
			}
			peer := &draftpeer.Peer{SessionID: "controller-remote"}
			peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
				if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
					return v2.PromptResponse{}, err
				}
				if err := peer.Echo(ctx, req.SessionId, message, "input"); err != nil {
					return v2.PromptResponse{}, err
				}
				oldID := firstID
				frames := []struct{ kind, id, text string }{{"agent_message", firstID, "first answer"}}
				if test.kind == "agent_thought" {
					oldID = oldThoughtID
					frames = []struct{ kind, id, text string }{{"agent_thought", oldID, "first thought"}, {"agent_message", firstID, "first answer"}}
				}
				if peer.Prompts.Load() == 2 {
					frames = []struct{ kind, id, text string }{{"agent_message", secondID, "second answer"}, {test.kind, oldID, "revised first narrative"}}
					if test.before {
						frames[0], frames[1] = frames[1], frames[0]
					}
				}
				if peer.Prompts.Load() == 2 && test.opaque && test.kind == "agent_thought" {
					frames = append([]struct{ kind, id, text string }{{"agent_thought", currentThoughtID, "current thought"}}, frames...)
				}
				for _, frame := range frames {
					if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": frame.kind, "messageId": frame.id, "content": []any{map[string]any{"type": "text", "text": frame.text}}}); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				if err := peer.State(ctx, req.SessionId, activity, "idle", "end_turn"); err != nil {
					return v2.PromptResponse{}, err
				}
				return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
			}
			registry, err := subagent.NewRegistry([]subagent.AgentConfig{{Name: "helper", HostedAdapterID: "fixture", ACPDraftSchema: acpdraft.SchemaTag}})
			if err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(Config{Registry: registry, EndpointResolver: peer})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Quiesce(context.Background()) })
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			parent, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			parent.CWD = t.TempDir()
			if _, err := manager.Activate(ctx, sdkcontroller.HandoffRequest{Session: parent, Agent: "helper"}); err != nil {
				t.Fatal(err)
			}
			forwarder := acpbridge.NewControllerForwarder(store)
			observedOldRevision := false
			for _, id := range []string{"first-turn", "second-turn"} {
				forwarding, err := forwarder.BeginControllerEvents(ctx, agent.ControllerEventForwardRequest{SessionRef: parent.SessionRef, ActiveSession: parent, TurnID: id, Publisher: discardSourcePublisher{}, IsUserEcho: func(event *session.Event) bool {
					return session.ProtocolSessionUpdateType(event) == string(session.ProtocolUpdateTypeUserMessage)
				}})
				if err != nil {
					t.Fatal(err)
				}
				turn, err := manager.RunTurn(ctx, sdkcontroller.TurnRequest{Session: parent, SessionRef: parent.SessionRef, TurnID: id, Input: "input", Stream: true, Observer: agent.SourceEventObserverFunc(func(ctx context.Context, event agent.SourceEvent) error {
					if event.Canonical != nil && session.EventText(event.Canonical) == "revised first narrative" {
						update := session.ProtocolUpdateOf(event.Canonical)
						oldID := firstID
						if test.kind == "agent_thought" {
							oldID = oldThoughtID
						}
						if update == nil || update.MessageID != oldID || !update.PreviouslyObserved {
							t.Errorf("old observation lost Session identity: %#v", event.Canonical)
						}
						observedOldRevision = true
					}
					return forwarding.ObserveSourceEvent(ctx, event)
				})})
				if err != nil {
					t.Fatal(err)
				}
				if err := turn.Handle.WaitCompletion(ctx); err != nil {
					t.Fatal(err)
				}
				if err := forwarding.Complete(ctx); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: parent.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			var answers []string
			for _, event := range loaded.Events {
				if session.EventTypeOf(event) != session.EventTypeAssistant {
					continue
				}
				message, ok := session.ModelMessageOf(event)
				if !ok {
					t.Fatal("canonical assistant lost model input")
				}
				answers = append(answers, message.TextContent())
				wantID := firstID
				if len(answers) == 2 {
					wantID = secondID
				}
				if got := session.EventMessageID(event); got != wantID {
					t.Fatalf("physical Session changed message ID: got %q, want %q", got, wantID)
				}
				if update := session.ProtocolUpdateOf(event); update != nil && update.PreviouslyObserved {
					t.Fatal("process-local observation became durable authority")
				}
			}
			if !observedOldRevision || len(answers) != 2 || answers[0] != "first answer" || answers[1] != "second answer" {
				t.Fatalf("old Session revision corrupted rebuilt model history: %#v observed=%t", answers, observedOldRevision)
			}
		})
	}
}
