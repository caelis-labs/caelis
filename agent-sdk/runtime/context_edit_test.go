package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestContextEditDurableProjectionAndUnknownCommit(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"append", "replace", "replace-empty"} {
		replace := mode != "append"
		for _, ack := range []string{"normal", "unknown", "conflict"} {
			t.Run(mode+"/"+ack, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				store := &contextEditAckStore{Store: newLogicalFileStore(t, sessionfile.Config{RootDir: root}), mode: ack}
				active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "context"})
				if err != nil {
					t.Fatal(err)
				}
				var actual []model.Message
				calls := 0
				llm := scriptedTestModel{generate: func(_ context.Context, r *model.Request) *model.Response {
					calls++
					actual = model.CloneMessages(r.Messages)
					return textResponse("done", model.Usage{})
				}}
				parts := []model.Part{model.NewTextPart("durable context"), model.NewJSONPart([]byte(`{"big":9007199254740993123456789}`)), model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceURL, URI: "https://example.invalid/image.png"}, "image/png", "image")}
				hooks := hook.Set{ModelPrepare: []hook.ModelPrepare{{Name: "edit", Apply: func(_ context.Context, e hook.PrepareContext, p hook.ModelPreparation) (hook.ModelPreparation, error) {
					edit := &hook.ContextEdit{Source: e.ContextSource}
					if replace {
						edit.ReplacePrefix = []model.Message{{Role: model.RoleUser, Parts: model.CloneParts(parts)}}
						if mode == "replace-empty" {
							edit.ReplacePrefix = []model.Message{}
						}
					} else {
						edit.Append = model.CloneParts(parts)
					}
					p.ContextEdit = edit
					return p, nil
				}}}}
				rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
				if err != nil {
					t.Fatal(err)
				}
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "original facts", AgentSpec: agent.AgentSpec{Model: contextEditModel{llm}}})
				if err != nil {
					t.Fatal(err)
				}
				err = run.Handle.WaitCompletion(t.Context())
				if ack == "conflict" {
					if err == nil || calls != 0 {
						t.Fatalf("payload conflict progressed: calls=%d err=%v", calls, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if calls != 1 || store.writes != 1 {
					t.Fatalf("provider=%d edit writes=%d", calls, store.writes)
				}
				live, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
				if err != nil {
					t.Fatal(err)
				}
				reopened := newLogicalFileStore(t, sessionfile.Config{RootDir: root})
				loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(live.Events, loaded.Events) {
					t.Fatal("full persisted payload changed on reopen")
				}
				var beforeFinal []*session.Event
				for _, e := range loaded.Events {
					if e.Type != session.EventTypeAssistant {
						beforeFinal = append(beforeFinal, e)
					}
					if e.Type == session.EventTypeUser && session.OriginalInputMessage(e).TextContent() != "original facts" {
						t.Fatal("source fact changed")
					}
				}
				projected := compact.PromptEventsFromLatestCompact(beforeFinal)
				var replay []model.Message
				for _, e := range projected {
					if m, ok := session.ModelMessageOf(e); ok {
						replay = append(replay, m)
					}
				}
				if !reflect.DeepEqual(actual, replay) {
					t.Fatalf("runtime/reopen messages differ\nlive=%#v\nreplay=%#v", actual, replay)
				}
				expected := 2
				if replace {
					expected = 1
				}
				if mode == "replace-empty" {
					expected = 0
				}
				if len(actual) != expected {
					t.Fatalf("messages=%d", len(actual))
				}
				assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
			})
		}
	}
}

type contextEditAckStore struct {
	*sessionfile.Store
	mode   string
	writes int
}

func (s *contextEditAckStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	isEdit := req.Event != nil && (req.Event.ContextEdit != nil || req.Event.Type == session.EventTypeCompact)
	if !isEdit {
		return s.Store.AppendEvent(ctx, req)
	}
	s.writes++
	if s.mode == "conflict" {
		req.Event = session.CloneEvent(req.Event)
		req.Event.Message = hookMessage("different payload")
	}
	e, err := s.Store.AppendEvent(ctx, req)
	if err == nil && s.mode != "normal" {
		return nil, errors.New("original append acknowledgement lost")
	}
	return e, err
}

func TestContextEditStaleAssemblyDoesNotAppendTwice(t *testing.T) {
	t.Parallel()
	store, active := newTestSessionService(t, "stale-edit")
	hookCalls, admissions, providers := 0, 0, 0
	llm := scriptedTestModel{generate: func(_ context.Context, r *model.Request) *model.Response {
		count := 0
		for _, m := range r.Messages {
			if m.TextContent() == "once" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("stale rebuild lost or duplicated edit: %v", r.Messages)
		}
		providers++
		return textResponse("done", model.Usage{})
	}}
	hooks := hook.Set{ModelPrepare: []hook.ModelPrepare{{Name: "append", Apply: func(_ context.Context, e hook.PrepareContext, p hook.ModelPreparation) (hook.ModelPreparation, error) {
		hookCalls++
		p.ContextEdit = &hook.ContextEdit{Source: e.ContextSource, Append: []model.Part{model.NewTextPart("once")}}
		return p, nil
	}}}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
		return agent.ModelRequestSnapshot{Model: llm, Validate: func(context.Context) (func(), error) { return func() {}, nil }, Admit: func(context.Context, agent.ModelRequestAdmission) error {
			admissions++
			if admissions == 1 {
				return agent.ErrModelRequestSnapshotStale
			}
			return nil
		}}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	edits := 0
	for _, e := range events {
		if e.ContextEdit != nil {
			edits++
		}
	}
	if hookCalls != 2 || admissions != 2 || providers != 1 || edits != 1 {
		t.Fatalf("hooks=%d admissions=%d provider=%d edits=%d", hookCalls, admissions, providers, edits)
	}
}

func TestContextEditRejectsUnpairedReplacementAndFutureCheckpoint(t *testing.T) {
	t.Parallel()
	m := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "one", Name: "effect", Args: `{}`}}, "")
	if err := session.ValidateContextMessages([]model.Message{m}); err == nil {
		t.Fatal("half tool group accepted")
	}
	var e session.Event
	if err := e.UnmarshalJSON([]byte(`{"schema":4,"type":"compact","_meta":{"compact":{"contract_version":"99"}}}`)); err == nil || !strings.Contains(err.Error(), "unsupported checkpoint") {
		t.Fatalf("future checkpoint error=%v", err)
	}
}

type contextEditModel struct{ scriptedTestModel }

func (contextEditModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, ParallelToolCalls: true, ImageInput: true}
}
