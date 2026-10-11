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
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
)

func TestDraftControllerResumedToolsPreserveCurrentTurnOwnership(t *testing.T) {
	for _, opaque := range []bool{false, true} {
		oldID, label := "old-tool", "ordinary/"
		if opaque {
			oldID, label = " current-tool ", "opaque/"
		}
		for _, test := range []struct {
			name, tool string
			prefix     map[string]any
			patch      map[string]any
			previous   bool
			barrier    bool
		}{
			{"old sparse update", oldID, nil, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": oldID, "status": "completed"}, true, false},
			{"old terminal update", oldID, nil, map[string]any{"sessionUpdate": "terminal_update", "terminalId": "old-terminal", "output": map[string]any{"data": "cmVwYWly"}}, true, false},
			{"new sparse update", "new-tool", nil, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "new-tool", "previouslyObserved": true, "_meta": map[string]any{"previouslyObserved": true}}, false, true},
			{"current sparse update", "current-tool", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "current-tool", "status": "in_progress"}, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "current-tool", "status": "completed"}, true, true},
		} {
			t.Run(label+test.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				peer := &draftpeer.Peer{SessionID: "resumed-controller"}
				resumed := false
				peer.OnResume = func(req v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
					resumed = true
					for _, value := range []map[string]any{
						{"sessionUpdate": "tool_call_update", "toolCallId": oldID, "status": "completed", "content": []any{map[string]any{"type": "terminal", "terminalId": "old-terminal"}}},
						{"sessionUpdate": "terminal_update", "terminalId": "old-terminal", "output": map[string]any{"data": "b2xk"}},
					} {
						if err := peer.Update(ctx, req.SessionId, value); err != nil {
							return v2.ResumeSessionResponse{}, err
						}
					}
					return v2.ResumeSessionResponse{}, nil
				}
				peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
					if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
						return v2.PromptResponse{}, err
					}
					if err := peer.Echo(ctx, req.SessionId, message, "input"); err != nil {
						return v2.PromptResponse{}, err
					}
					if opaque {
						if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "current-tool", "status": "in_progress"}); err != nil {
							return v2.PromptResponse{}, err
						}
					}
					if test.prefix != nil {
						if err := peer.Update(ctx, req.SessionId, test.prefix); err != nil {
							return v2.PromptResponse{}, err
						}
					}
					if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_message", "messageId": "current-answer", "content": []any{map[string]any{"type": "text", "text": "current answer"}}}); err != nil {
						return v2.PromptResponse{}, err
					}
					if err := peer.Update(ctx, req.SessionId, test.patch); err != nil {
						return v2.PromptResponse{}, err
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
				parent.Controller = session.ControllerBinding{Kind: session.ControllerKindACP, AgentName: "helper", RemoteSessionID: peer.SessionID}
				if _, err := manager.Activate(ctx, sdkcontroller.HandoffRequest{Session: parent, Agent: "helper"}); err != nil {
					t.Fatal(err)
				}
				if !resumed {
					t.Fatal("controller did not resume historical tool identities")
				}
				forwarding, err := acpbridge.NewControllerForwarder(store).BeginControllerEvents(ctx, agent.ControllerEventForwardRequest{SessionRef: parent.SessionRef, ActiveSession: parent, TurnID: "current-turn", Publisher: discardSourcePublisher{}, IsUserEcho: func(event *session.Event) bool {
					return session.ProtocolSessionUpdateType(event) == string(session.ProtocolUpdateTypeUserMessage)
				}})
				if err != nil {
					t.Fatal(err)
				}
				observed := false
				turn, err := manager.RunTurn(ctx, sdkcontroller.TurnRequest{Session: parent, SessionRef: parent.SessionRef, TurnID: "current-turn", Input: "input", Stream: true, Observer: agent.SourceEventObserverFunc(func(ctx context.Context, event agent.SourceEvent) error {
					update := session.ProtocolUpdateOf(event.Canonical)
					if update != nil && update.ToolCallID == test.tool && update.PreviouslyObserved == test.previous {
						if test.name == "old terminal update" {
							output, ok := acpmeta.ReadTerminalOutput(update.Meta)
							observed = ok && output.Data == "repair" && output.Replacement
						} else {
							observed = true
						}
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
				loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: parent.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				var answers []string
				for _, event := range loaded.Events {
					if update := session.ProtocolUpdateOf(event); update != nil && update.PreviouslyObserved {
						t.Fatal("private tool observation became durable authority")
					}
					if session.IsUIOnly(event) || session.EventTypeOf(event) != session.EventTypeAssistant {
						continue
					}
					if message, ok := session.ModelMessageOf(event); ok {
						answers = append(answers, message.TextContent())
					}
				}
				if !observed || (!test.barrier && (len(answers) != 1 || answers[0] != "current answer")) || (test.barrier && len(answers) != 0) {
					t.Fatalf("resumed tool corrupted canonical model history: observed=%t answers=%#v", observed, answers)
				}
			})
		}
	}
}
