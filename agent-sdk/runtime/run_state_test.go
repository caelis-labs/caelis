package runtime

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

type indexedRunStateStore struct{ *sessionfile.Store }

func (*indexedRunStateStore) Events(context.Context, session.EventsRequest) ([]*session.Event, error) {
	panic("RunState loaded transcript")
}

func TestResumeMetadataPreservesRuntimeProducedModelContext(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	run := func(sessions session.Service, input string) []model.Message {
		t.Helper()
		probe := &recoveryCaptureModel{}
		rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: input, AgentSpec: agent.AgentSpec{Name: "chat", Model: probe}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := drainRunnerEvents(t, result.Handle); err != nil {
			t.Fatal(err)
		}
		return model.CloneMessages(probe.messages)
	}
	first := run(store, "before reconnect")
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	state, err := (&Runtime{sessions: &indexedRunStateStore{reopened}}).persistedRunState(t.Context(), active.SessionRef, "")
	if err != nil || state.Status != agent.RunLifecycleStatusCompleted {
		t.Fatalf("recovered state=%+v error=%v", state, err)
	}
	metadata, err := reopened.EventMetadataPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil || metadata.NextSeq == 0 {
		t.Fatalf("metadata=%+v error=%v", metadata, err)
	}
	want := append(first, model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "after reconnect"))
	got := run(sessionfile.NewStore(sessionfile.Config{RootDir: root}), "after reconnect")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt context=%#v, want runtime-produced %#v", got, want)
	}
}

func TestIndexedRunStateMatchesFullHistoryAndPreservesModelContext(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	message := model.NewTextMessage(model.RoleUser, "canonical user message remains unchanged")
	if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Message: &message}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	appendRun := func(id string, revision uint64, status session.ExecutionStatus) {
		t.Helper()
		run := session.NormalizeExecutionRecord(session.ExecutionRecord{Kind: session.JournalKindRun, SessionID: active.SessionID, RunID: id, Revision: revision, Status: status, Reason: "reason"})
		if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{ID: fmt.Sprintf("%s-%d", id, revision), Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Journal: &session.ExecutionJournalEntry{Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindRun, Execution: &run}}}); err != nil {
			t.Fatal(err)
		}
	}
	appendPause := func(status session.PauseTokenStatus, revision uint64) {
		token := &session.PauseToken{Schema: session.ExecutionJournalSchemaVersion, TokenID: "pause", SessionID: active.SessionID, RunID: "first", TurnID: "turn", Revision: revision, Status: status, ToolCallID: "call", ReviewText: "review"}
		if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Journal: &session.ExecutionJournalEntry{Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindPauseToken, PauseToken: token}}}); err != nil {
			t.Fatal(err)
		}
	}
	compare := func() {
		t.Helper()
		// The wrapper hides only the optional index for the reference reader. A
		// fresh file Store exercises durable index reload on every comparison.
		fast := &Runtime{sessions: &indexedRunStateStore{sessionfile.NewStore(sessionfile.Config{RootDir: root})}}
		base := &Runtime{sessions: struct{ session.Service }{store}}
		for _, id := range []string{"", "first", "second", "missing"} {
			got, gotErr := fast.persistedRunState(t.Context(), active.SessionRef, id)
			want, wantErr := base.persistedRunState(t.Context(), active.SessionRef, id)
			if !reflect.DeepEqual(got, want) || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
				t.Fatalf("run=%s got=%+v/%v want=%+v/%v", id, got, gotErr, want, wantErr)
			}
		}
	}
	compare()
	for n, status := range []session.ExecutionStatus{session.ExecutionPrepared, session.ExecutionStarted, session.ExecutionWaitingApproval} {
		appendRun("first", uint64(n+1), status)
		compare()
	}
	appendPause(session.PauseTokenPending, 1)
	compare()
	appendPause(session.PauseTokenResolved, 2)
	compare()
	appendRun("second", 1, session.ExecutionFailed)
	compare()
	for n, status := range []session.ExecutionStatus{session.ExecutionSucceeded, session.ExecutionCancelled, session.ExecutionInterrupted, session.ExecutionUnknownOutcome} {
		appendRun("first", uint64(n+4), status)
		compare()
	}
	after, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("metadata/recovery changed canonical model-visible history")
	}
}
