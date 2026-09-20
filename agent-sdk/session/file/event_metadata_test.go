package file

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestEventMetadataSurvivesRestartAndBoundsPayloadReads(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 400)
	req := session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay, Limit: 1000}
	if page, err := store.EventMetadataPage(t.Context(), req); err != nil || len(page.Events) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	cold := NewStore(Config{RootDir: store.rootDir})
	cold.eventMetadataLineRead = func(string, int, int64) { t.Fatal("restart rescanned indexed source") }
	if _, err := cold.EventMetadataPage(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	var lines []int
	cold.eventPageLineRead = func(_ string, line int, _ int64) { lines = append(lines, line) }
	req.AfterSeq, req.Limit = 398, 2
	page, err := cold.EventsPage(t.Context(), req)
	if err != nil || len(page.Events) != 2 || page.Events[0].Seq != 399 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if len(lines) > eventMetadataCheckpointStride || lines[len(lines)-1] != 400 {
		t.Fatalf("payload lines=%v", lines)
	}
	appendLifecycleEvents(t, store, active.SessionRef, 400, 2)
	var indexed []int
	cold.eventMetadataLineRead = func(_ string, line int, _ int64) { indexed = append(indexed, line) }
	req.AfterSeq, req.Limit = 400, 100
	if page, err := cold.EventMetadataPage(t.Context(), req); err != nil || len(page.Events) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if !reflect.DeepEqual(indexed, []int{401, 402}) {
		t.Fatalf("append indexed=%v", indexed)
	}
}

func TestEventMetadataInvalidationAndCancellation(t *testing.T) {
	for _, change := range []string{"missing cache", "corrupt cache", "truncate", "rewrite", "replace", "partial tail", "cancelled build"} {
		t.Run(change, func(t *testing.T) {
			store, active := newEventPageIndexFixture(t, 4)
			path, err := store.resolveWritePath(active)
			if err != nil {
				t.Fatal(err)
			}
			log := eventLogPath(path)
			req := session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable}
			initial, err := store.EventMetadataPage(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			want := uint64(4)
			switch change {
			case "missing cache":
				if err := os.Remove(log + ".metadata"); err != nil {
					t.Fatal(err)
				}
			case "corrupt cache":
				if err := os.WriteFile(log+".metadata", []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "truncate":
				lines := strings.SplitAfter(string(raw), "\n")
				raw = []byte(lines[0] + lines[1])
				want = 2
				if err := os.WriteFile(log, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "rewrite", "replace":
				raw = []byte(strings.ReplaceAll(string(raw), `"seq":4`, `"seq":9`))
				if change == "replace" {
					if err := os.Remove(log); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(log, raw, 0600); err != nil {
					t.Fatal(err)
				}
				at := time.Now().Add(time.Second)
				if err := os.Chtimes(log, at, at); err != nil {
					t.Fatal(err)
				}
			case "partial tail":
				f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString(`{"seq":5`); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			case "cancelled build":
				if err := os.Remove(log + ".metadata"); err != nil {
					t.Fatal(err)
				}
				store = NewStore(Config{RootDir: store.rootDir})
				ctx, cancel := context.WithCancel(t.Context())
				store.eventMetadataLineRead = func(string, int, int64) { cancel() }
				if _, err := store.EventMetadataPage(ctx, req); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error=%v", err)
				}
				store.eventMetadataLineRead = nil
			}
			cold := NewStore(Config{RootDir: store.rootDir})
			got, err := cold.EventMetadataPage(t.Context(), req)
			if err != nil || got.NextSeq != want && change != "rewrite" && change != "replace" {
				t.Fatalf("page=%+v err=%v", got, err)
			}
			if change == "rewrite" || change == "replace" {
				if got.NextSeq != 9 {
					t.Fatal("stale sequence index")
				}
			} else if want == 4 && !reflect.DeepEqual(initial, got) {
				t.Fatalf("changed projection: %+v", got)
			}
		})
	}
}

func TestEventMetadataVisibilityMatchesCanonicalReaders(t *testing.T) {
	// Indexing must not infer a second set of projection rules for reviewed
	// approvals or mirrored records.
	cases := []*session.Event{
		{Schema: session.EventSchemaVersion, Seq: 1, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Scope: &session.EventScope{TurnID: "turn"}},
		{Schema: session.EventSchemaVersion, Seq: 2, Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindPauseToken, PauseToken: &session.PauseToken{Status: session.PauseTokenResolved, ToolCallID: "call", ReviewText: "review", TurnID: "original"}}},
		{Schema: session.EventSchemaVersion, Seq: 3, Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindPauseToken, PauseToken: &session.PauseToken{Status: session.PauseTokenPending, ToolCallID: "call", ReviewText: "review"}}},
		{Schema: session.EventSchemaVersion, Seq: 4, Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror, ChildOrigin: &session.EventChildOrigin{Scope: session.EventChildScopeSubagent}},
	}
	for _, event := range cases {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		got, err := eventMetadataFromJSON(raw, "test", 1)
		if err != nil {
			t.Fatal(err)
		}
		if got.Canonical != session.IsCanonicalHistoryEvent(event) || got.Replay != session.IsClientReplayEvent(event) || got.ReviewedApproval != (session.ResolvedApprovalReview(event) != nil) {
			t.Fatalf("metadata=%+v event=%+v", got, event)
		}
	}
}

func TestEventMetadataRecoversCommittedWALBeforeReadingCachedIndex(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 4)
	req := session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable}
	if _, err := store.EventMetadataPage(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	store.transactionFault = func(phase string) error {
		if phase == "after_commit" {
			return errors.New("simulated loss after WAL commit")
		}
		return nil
	}
	run := session.NormalizeExecutionRecord(session.ExecutionRecord{Kind: session.JournalKindRun, SessionID: active.SessionID, RunID: "recovered", Revision: 1, Status: session.ExecutionSucceeded})
	if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal,
		Journal: &session.ExecutionJournalEntry{Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindRun, Execution: &run},
	}}); !session.IsCommitted(err) {
		t.Fatalf("append error=%v, want committed WAL", err)
	}
	reopened := NewStore(Config{RootDir: store.rootDir})
	got, err := reopened.RunJournal(t.Context(), active.SessionRef, "")
	if err != nil || got.Run == nil || got.Run.RunID != "recovered" {
		t.Fatalf("recovered journal=%+v error=%v", got, err)
	}
	page, err := reopened.EventMetadataPage(t.Context(), req)
	if err != nil || page.NextSeq != 5 {
		t.Fatalf("recovered metadata=%+v error=%v", page, err)
	}
	if _, err := os.Stat(reopened.transactionRecoveryMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("recovery marker=%v, want removed", err)
	}
}
