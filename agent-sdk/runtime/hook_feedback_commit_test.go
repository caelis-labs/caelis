package runtime

import (
	"context"
	"errors"
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

type feedbackAckStore struct {
	*sessionfile.Store
	mode   string
	writes atomic.Int64
}

func (s *feedbackAckStore) AppendEvent(ctx context.Context, r session.AppendEventRequest) (*session.Event, error) {
	if r.Event != nil && strings.HasPrefix(r.Event.ID, "hook-feedback:") {
		s.writes.Add(1)
		if s.mode == "failed" {
			return nil, errors.New("feedback not committed")
		}
		if s.mode == "conflict" {
			r.Event = session.CloneEvent(r.Event)
			r.Event.Message.Parts[0].Text.Text = "different payload"
		}
		if _, err := s.Store.AppendEvent(ctx, r); err != nil {
			return nil, err
		}
		return nil, errors.New("feedback ACK lost")
	}
	return s.Store.AppendEvent(ctx, r)
}
func TestHooksFeedbackBudgetConsumesOnlyConfirmedCommits(t *testing.T) {
	for _, mode := range []string{"committed", "failed", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			store := &feedbackAckStore{Store: sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()}), mode: mode}
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "feedback"})
			if err != nil {
				t.Fatal(err)
			}
			var providers atomic.Int64
			llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
				providers.Add(1)
				return textResponse("candidate", model.Usage{})
			}}
			h := hook.Set{CompletionLimit: 2, Completion: []hook.Completion{{Name: "continue", Apply: func(context.Context, hook.OutputContext, model.Message) (hook.CompletionDecision, error) {
				return hook.CompletionDecision{Kind: hook.CompletionContinue, Feedback: []model.Part{model.NewTextPart("feedback")}}, nil
			}}}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Model: llm}})
			if err != nil {
				t.Fatal(err)
			}
			completion := run.Handle.WaitCompletion(t.Context())
			if completion == nil {
				t.Fatal("failure or bounded rejection lost")
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			for _, e := range events {
				if strings.HasPrefix(e.ID, "hook-feedback:") {
					if ids[e.ID] {
						t.Fatal("duplicate feedback ID")
					}
					ids[e.ID] = true
				}
			}
			if mode == "committed" {
				if len(ids) != 2 || providers.Load() != 3 || store.writes.Load() != 2 || !strings.Contains(completion.Error(), "completion_limit") {
					t.Fatalf("feedback budget bypass/replay: ids=%d provider=%d writes=%d completion=%v", len(ids), providers.Load(), store.writes.Load(), completion)
				}
			} else {
				want := 0
				if mode == "conflict" {
					want = 1
				}
				if len(ids) != want || providers.Load() != 1 || store.writes.Load() != 1 {
					t.Fatalf("unconfirmed commit continued/replayed: ids=%d provider=%d writes=%d", len(ids), providers.Load(), store.writes.Load())
				}
			}
		})
	}
}
