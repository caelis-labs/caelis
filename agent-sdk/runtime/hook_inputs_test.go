package runtime

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestInputHooksEarlyCompletionFileReopen(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "early"})
	if err != nil {
		t.Fatal(err)
	}
	var inputCalls, turnCalls, providerCalls, completeCalls atomic.Int64
	hooks := hook.Set{
		Input: []hook.Input{{Name: "normalize", Apply: func(ctx context.Context, e hook.InputContext, v hook.InputView) (hook.InputDecision, error) {
			inputCalls.Add(1)
			if e.RunID == "" || e.TurnID == "" {
				t.Error("missing trusted identity")
			}
			m := model.NewTextMessage(model.RoleAssistant, "early")
			v.Message.Parts[0].Text.Text = "effective"
			return hook.InputDecision{Message: v.Message, Response: &m}, nil
		}}},
		TurnStart: []hook.TurnStart{{Name: "start", Apply: func(context.Context, hook.TurnContext) (hook.TurnStartDecision, error) {
			turnCalls.Add(1)
			return hook.TurnStartDecision{}, nil
		}}},
		Completion: []hook.Completion{{Name: "review", Apply: func(context.Context, hook.OutputContext, model.Message) (hook.CompletionDecision, error) {
			completeCalls.Add(1)
			m := model.NewTextMessage(model.RoleAssistant, "accepted")
			return hook.CompletionDecision{Kind: hook.CompletionReplace, Message: &m}, nil
		}}},
	}
	llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
		providerCalls.Add(1)
		return textResponse("unreachable", model.Usage{})
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "original user authority", AgentSpec: agent.AgentSpec{Name: "chat", Model: llm}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	live, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live.Events, reopened.Events) {
		t.Fatal("full events changed on reopen")
	}
	var user, final *session.Event
	for _, e := range reopened.Events {
		if e.Type == session.EventTypeUser {
			user = e
		}
		if e.Type == session.EventTypeAssistant {
			final = e
		}
	}
	if user == nil || user.Message.TextContent() != "effective" || session.OriginalInputMessage(user).TextContent() != "original user authority" || final == nil || final.Message.TextContent() != "accepted" {
		t.Fatalf("user=%#v final=%#v", user, final)
	}
	if inputCalls.Load() != 1 || turnCalls.Load() != 1 || providerCalls.Load() != 0 || completeCalls.Load() != 1 {
		t.Fatalf("calls input=%d turn=%d provider=%d completion=%d", inputCalls.Load(), turnCalls.Load(), providerCalls.Load(), completeCalls.Load())
	}
}

func TestCompletionFeedbackHasSharedBoundAndNoRejectedFinal(t *testing.T) {
	store, active := newTestSessionService(t, "completion-bound")
	var calls atomic.Int64
	llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
		calls.Add(1)
		return textResponse("rejected candidate", model.Usage{})
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{Completion: []hook.Completion{{Name: "continue", Apply: func(context.Context, hook.OutputContext, model.Message) (hook.CompletionDecision, error) {
		return hook.CompletionDecision{Kind: hook.CompletionContinue, Feedback: []model.Part{model.NewTextPart("try again")}}, nil
	}}}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Name: "chat", Model: llm}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err == nil || !strings.Contains(err.Error(), "completion_limit") {
		t.Fatalf("error=%v", err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	feedback := 0
	for _, e := range loaded.Events {
		if e.Type == session.EventTypeAssistant {
			t.Fatal("rejected final committed")
		}
		if e.Type == session.EventTypeContext {
			feedback++
			if e.Actor.Kind != session.ActorKindSystem {
				t.Fatal("feedback became user authority")
			}
		}
	}
	if calls.Load() != 3 || feedback != 2 {
		t.Fatalf("model=%d feedback=%d", calls.Load(), feedback)
	}
}
