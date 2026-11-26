package file

import (
	"context"
	"database/sql"
	"errors"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionOpensTaskTableOnlyOnTaskAccess(t *testing.T) {
	store := NewStore(Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", store.sessionIndexPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hasTaskTable := func() bool {
		t.Helper()
		var found bool
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'tasks')").Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	tasks := NewTaskStore(store)
	if hasTaskTable() {
		t.Fatal("Session access initialized Task storage")
	}
	if _, err := tasks.ListSession(t.Context(), active.SessionRef); err != nil {
		t.Fatal(err)
	}
	if !hasTaskTable() {
		t.Fatal("Task access did not initialize Task storage")
	}
}

func TestTaskStoreRevisionCAS(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backing := NewStore(Config{RootDir: root})
	store := NewTaskStore(backing)
	created, err := store.Put(context.Background(), task.PutRequest{
		Entry: &task.Entry{
			TaskID: "task-cas", Kind: task.KindCommand, Session: taskSessionRef("sess-cas"), State: task.StateRunning, Running: true,
			ContextUsage: &task.ContextUsageRecord{
				Snapshot:   session.ContextUsageSnapshot{Size: 200000, Used: 42000},
				Invocation: session.EventInvocation{Provider: "codex", Model: "gpt-test"},
			},
		},
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatalf("Put(create) error = %v", err)
	}
	if created.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", created.Revision)
	}
	stale := task.CloneEntry(created)
	created.Title = "current"
	updated, err := store.Put(context.Background(), task.PutRequest{Entry: created, ExpectedRevision: created.Revision})
	if err != nil {
		t.Fatalf("Put(update) error = %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("updated revision = %d, want 2", updated.Revision)
	}
	stale.Title = "stale"
	_, err = store.Put(context.Background(), task.PutRequest{Entry: stale, ExpectedRevision: stale.Revision})
	var revisionErr *task.RevisionConflictError
	if !errors.As(err, &revisionErr) || revisionErr.Actual != 2 {
		t.Fatalf("stale Put error = %v, want revision conflict at 2", err)
	}

	db, err := backing.openTaskIndex()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE tasks SET lease_id = 'old-lease', lease_owner_id = 'old-worker', lease_revision = 10 WHERE task_id = 'task-cas'`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	reopenedStore := NewTaskStore(NewStore(Config{RootDir: root}))
	reopened, err := reopenedStore.Get(context.Background(), "task-cas")
	if err != nil {
		t.Fatalf("reopened Get() error = %v", err)
	}
	if reopened.Revision != 2 || reopened.Title != "current" {
		t.Fatalf("reopened task = %#v, want persisted revision 2 and current title", reopened)
	}
	if reopened.ContextUsage == nil || reopened.ContextUsage.Snapshot.Used != 42000 || reopened.ContextUsage.Snapshot.Size != 200000 ||
		reopened.ContextUsage.Invocation.Provider != "codex" || reopened.ContextUsage.Invocation.Model != "gpt-test" {
		t.Fatalf("reopened context usage = %#v, want persisted ACP gauge", reopened.ContextUsage)
	}
	reopened.Title = "after restart"
	updated, err = reopenedStore.Put(t.Context(), task.PutRequest{Entry: reopened, ExpectedRevision: reopened.Revision})
	if err != nil || updated == nil || updated.Revision != 3 || updated.Title != "after restart" {
		t.Fatalf("Put(reopened task) = %#v, %v, want next revision", updated, err)
	}
}

func TestTaskStoreDetachedSubagentActivityRequiresNextGeneration(t *testing.T) {
	for _, invalid := range []string{"", "same_generation", "skipped_generation", "missing_identity", "terminal", "foreign_session", "running_predecessor"} {
		t.Run(invalid, func(t *testing.T) {
			store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
			seed := &task.Entry{
				TaskID: "child", Kind: task.KindSubagent, Session: taskSessionRef("external-session"),
				State: task.StateCompleted, Spec: map[string]any{"turn_seq": int64(1)},
				Metadata: map[string]any{"child_activity_id": "first", "child_activity_generation": int64(1)},
			}
			if invalid == "running_predecessor" {
				seed.State, seed.Running = task.StateRunning, true
			}
			current, err := store.Put(t.Context(), task.PutRequest{Entry: seed})
			if err != nil {
				t.Fatal(err)
			}
			entry := task.CloneEntry(current)
			entry.State, entry.Running = task.StateRunning, true
			entry.Spec["turn_seq"] = int64(2)
			entry.Metadata["child_activity_id"], entry.Metadata["child_activity_generation"] = "second", int64(2)
			switch invalid {
			case "same_generation":
				entry.Spec["turn_seq"] = int64(1)
			case "skipped_generation":
				entry.Spec["turn_seq"] = int64(3)
			case "missing_identity":
				delete(entry.Metadata, "child_activity_id")
			case "terminal":
				entry.State, entry.Running = task.StateCompleted, false
			case "foreign_session":
				entry.Session.SessionID = "other-session"
			}
			ctx := session.ContextWithControlMutation(t.Context(), session.ControlMutationPurposeSubagentActivity)
			_, err = store.Put(ctx, task.PutRequest{Entry: entry, ExpectedRevision: current.Revision})
			if invalid == "" && err != nil {
				t.Fatalf("next activity rejected: %v", err)
			}
			if invalid != "" && err == nil {
				t.Fatalf("invalid activity %q accepted", invalid)
			}
		})
	}
}

func TestTaskStoreDetachedSubagentUsagePreservesActivity(t *testing.T) {
	for _, state := range []task.State{task.StateRunning, task.StateCompleted, task.StateUnknownOutcome} {
		for _, mutation := range []string{"", "identity", "generation", "state", "running", "result", "cancel", "spec", "cursor", "no_gauge"} {
			t.Run(string(state)+"/"+mutation, func(t *testing.T) {
				store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
				seed := &task.Entry{
					TaskID: "child", Kind: task.KindSubagent, Session: taskSessionRef("external-session"),
					State: state, Running: state == task.StateRunning,
					Spec:     map[string]any{"turn_seq": int64(1), "retained": "spec"},
					Metadata: map[string]any{"child_activity_id": "first", "child_activity_generation": int64(1), "cancel_turn_seq": int64(1)},
					Result:   map[string]any{"retained": "result"},
				}
				current, err := store.Put(t.Context(), task.PutRequest{Entry: seed})
				if err != nil {
					t.Fatal(err)
				}
				// Use the actual decoded canonical entry, as the Runtime writer does.
				current, err = store.Get(t.Context(), current.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				entry := task.CloneEntry(current)
				entry.ContextUsage = &task.ContextUsageRecord{Snapshot: session.ContextUsageSnapshot{Size: 200000, Used: 8000}}
				entry.UpdatedAt = time.Now()
				switch mutation {
				case "identity":
					entry.Metadata["child_activity_id"] = "other"
				case "generation":
					entry.Metadata["child_activity_generation"] = int64(2)
				case "state":
					entry.State = task.StateFailed
				case "running":
					entry.Running = !entry.Running
				case "result":
					entry.Result["retained"] = "changed"
				case "cancel":
					delete(entry.Metadata, "cancel_turn_seq")
				case "spec":
					entry.Spec["retained"] = "changed"
				case "cursor":
					entry.EventCursor++
				case "no_gauge":
					entry.ContextUsage = nil
				}
				ctx := session.ContextWithControlMutation(t.Context(), session.ControlMutationPurposeSubagentActivity)
				_, err = store.Put(ctx, task.PutRequest{Entry: entry, ExpectedRevision: current.Revision})
				if mutation == "" {
					if err != nil {
						t.Fatalf("same-activity usage rejected: %v", err)
					}
					got, err := store.Get(t.Context(), current.TaskID)
					if err != nil || got.ContextUsage == nil || got.ContextUsage.Snapshot.Used != 8000 {
						t.Fatalf("durable gauge = %#v, %v", got, err)
					}
				} else {
					if err == nil {
						t.Fatal("usage mutation changed another activity field")
					}
					got, loadErr := store.Get(t.Context(), current.TaskID)
					if loadErr != nil || !reflect.DeepEqual(got, current) {
						t.Fatalf("rejected mutation changed durable Task: %#v, %v", got, loadErr)
					}
				}
			})
		}
	}
}

func TestTaskStoreAllowsOnlySubagentCompletionWithoutLocalSessionDocument(t *testing.T) {
	t.Parallel()

	store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
	completionCtx := session.ContextWithControlMutation(
		context.Background(),
		session.ControlMutationPurposeSubagentCompletion,
	)
	for _, test := range []struct {
		name     string
		entry    *task.Entry
		existing bool
	}{
		{
			name: "create",
			entry: &task.Entry{
				TaskID: "detached-create", Kind: task.KindSubagent,
				Session: taskSessionRef("session-owned-by-another-store"),
				State:   task.StateCompleted,
			},
		},
		{
			name:     "command",
			existing: true,
			entry: &task.Entry{
				TaskID: "detached-command", Kind: task.KindCommand,
				Session: taskSessionRef("session-owned-by-another-store"),
				State:   task.StateCompleted,
			},
		},
		{
			name:     "running",
			existing: true,
			entry: &task.Entry{
				TaskID: "detached-running", Kind: task.KindSubagent,
				Session: taskSessionRef("session-owned-by-another-store"),
				State:   task.StateRunning, Running: true,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			expected := uint64(0)
			if test.existing {
				created, err := store.Put(context.Background(), task.PutRequest{Entry: test.entry})
				if err != nil {
					t.Fatalf("Put(%s seed) error = %v", test.name, err)
				}
				expected = created.Revision
			}
			if _, err := store.Put(completionCtx, task.PutRequest{
				Entry: test.entry, ExpectedRevision: expected,
			}); err == nil {
				t.Fatalf("Put(%s completion mutation) error = nil, want detached completion rejection", test.name)
			}
		})
	}

	entry, err := store.Put(context.Background(), task.PutRequest{
		Entry: &task.Entry{
			TaskID: "detached-completion", Kind: task.KindSubagent,
			Session: taskSessionRef("session-owned-by-another-store"),
			State:   task.StateRunning, Running: true,
		},
	})
	if err != nil {
		t.Fatalf("Put(create) error = %v", err)
	}
	entry.State = task.StateCompleted
	entry.Running = false
	if _, err := store.Put(completionCtx, task.PutRequest{Entry: entry, ExpectedRevision: entry.Revision}); err != nil {
		t.Fatalf("Put(subagent completion) error = %v", err)
	}

	entry.Revision++
	otherControlCtx := session.ContextWithControlMutation(
		context.Background(),
		session.ControlMutationPurposeApproval,
	)
	if _, err := store.Put(otherControlCtx, task.PutRequest{Entry: entry, ExpectedRevision: entry.Revision}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Put(other detached Control purpose) error = %v, want session not found", err)
	}
}

func TestTaskStorePersistsTypedFailureDiagnostic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root}))
	created, err := store.Put(context.Background(), task.PutRequest{
		Entry: &task.Entry{
			TaskID:            "failed-subagent",
			Kind:              task.KindSubagent,
			Session:           taskSessionRef("failed-session"),
			State:             task.StateFailed,
			FailureDiagnostic: " subagent prompt failed ",
			Result: map[string]any{
				"state": "failed",
				"error": "subagent prompt failed",
			},
		},
	})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if got := created.FailureDiagnostic; got != "subagent prompt failed" {
		t.Fatalf("created FailureDiagnostic = %q, want bounded diagnostic", got)
	}

	reopened, err := NewTaskStore(NewStore(Config{RootDir: root})).Get(context.Background(), created.TaskID)
	if err != nil {
		t.Fatalf("reopened Get() error = %v", err)
	}
	if got := reopened.FailureDiagnostic; got != "subagent prompt failed" {
		t.Fatalf("reopened FailureDiagnostic = %q, want bounded diagnostic", got)
	}
}

func TestEnsureTaskIndexColumnsAddsCurrentFields(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy-index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE tasks (task_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureTaskIndexColumns(db); err != nil {
		t.Fatalf("ensureTaskIndexColumns() error = %v", err)
	}
	rows, err := db.Query(`PRAGMA table_info(tasks)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "failure_diagnostic" || name == "context_usage_json" {
			found[name] = typ == "TEXT" && notNull == 1
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"failure_diagnostic", "context_usage_json"} {
		if !found[name] {
			t.Fatalf("%s column was not added as required TEXT", name)
		}
	}
}

func TestTaskStoreRejectsStaleSessionFenceAfterTakeover(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0).UTC()
	backing, priorHostFences := NewStoreWithPriorHostFences(
		Config{RootDir: t.TempDir(), Clock: func() time.Time { return now }},
		func(context.Context) (func(), bool) { return func() {}, true },
	)
	active, err := backing.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "task-fence", PreferredSessionID: "task-fence",
	})

	if err != nil {
		t.Fatal(err)
	}
	fenceA, err := backing.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctxA := session.ContextWithRuntimeFence(context.Background(), fenceA)
	tasks := NewTaskStore(backing)
	created, err := tasks.Put(ctxA, task.PutRequest{Entry: &task.Entry{
		TaskID: "fenced-task", Kind: task.KindSubagent, Session: active.SessionRef, State: task.StateRunning, Running: true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	fenceB, err := priorHostFences.ReplacePriorHostSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := task.CloneEntry(created)
	stale.Title = "stale host write"
	if _, err := tasks.Put(ctxA, task.PutRequest{Entry: stale, ExpectedRevision: created.Revision}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("stale task Put error = %v, want Session fence conflict", err)
	}
	current := task.CloneEntry(created)
	current.Title = "new owner write"
	ctxB := session.ContextWithRuntimeFence(context.Background(), fenceB)
	if _, err := tasks.Put(ctxB, task.PutRequest{Entry: current, ExpectedRevision: created.Revision}); err != nil {
		t.Fatalf("new owner task Put error = %v", err)
	}
}

func TestTaskStoreReturnsCommittedEntryAfterPostCommitFailure(t *testing.T) {
	t.Parallel()
	backing := NewStore(Config{RootDir: t.TempDir()})
	tasks := NewTaskStore(backing)
	backing.transactionFault = func(phase string) error {
		if phase == "task_index_post_commit" {
			return errors.New("forced task index directory sync failure")
		}
		return nil
	}
	persisted, err := tasks.Put(context.Background(), task.PutRequest{Entry: &task.Entry{
		TaskID: "task-committed", Kind: task.KindSubagent, Session: taskSessionRef("task-committed-session"), State: task.StateRunning,
	}})
	if !session.IsCommitted(err) {
		t.Fatalf("Put error = %v, want committed outcome", err)
	}
	if persisted == nil || persisted.Revision != 1 {
		t.Fatalf("persisted entry = %#v, want exact revision 1", persisted)
	}
	backing.transactionFault = nil
	durable, loadErr := tasks.Get(context.Background(), "task-committed")
	if loadErr != nil || durable.Revision != persisted.Revision {
		t.Fatalf("durable entry = %#v, %v; want committed revision", durable, loadErr)
	}
}

func TestTaskStoreUpsertCompletedTaskKeepsCanonicalResultInIndex(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	entry := &task.Entry{
		TaskID:    "task-1",
		Kind:      task.KindCommand,
		Session:   taskSessionRef("sess-1"),
		Title:     "RUN_COMMAND echo hi",
		State:     task.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(10, 0),
		UpdatedAt: time.Unix(20, 0),
		Result: map[string]any{
			"stdout":         "hello\n",
			"stderr":         "warn\n",
			"output":         "transient output\n",
			"text":           "transient text\n",
			"latest_output":  "transient latest\n",
			"output_preview": "transient preview\n",
			"result":         "hello\nwarn\n",
			"exit_code":      0,
			"state":          "completed",
		},
		Terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  "exec-1",
			TerminalID: "term-1",
		},
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	listed, err := store.ListSession(context.Background(), taskSessionRef("sess-1"))
	if err != nil {
		t.Fatalf("ListSession() error = %v", err)
	}
	if got, want := len(listed), 1; got != want {
		t.Fatalf("len(listed) = %d, want %d", got, want)
	}
	if _, ok := listed[0].Result["stdout"]; ok {
		t.Fatalf("ListSession result unexpectedly contains stdout: %#v", listed[0].Result)
	}
	if _, ok := listed[0].Result["stderr"]; ok {
		t.Fatalf("ListSession result unexpectedly contains stderr: %#v", listed[0].Result)
	}
	if got, _ := listed[0].Result["result"].(string); got != "hello\nwarn\n" {
		t.Fatalf("ListSession result = %q, want canonical result", got)
	}
	assertNoTransientTaskResultKeys(t, listed[0].Result)

	got, err := store.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	assertNoTransientTaskResultKeys(t, got.Result)
	if gotResult, _ := got.Result["result"].(string); gotResult != "hello\nwarn\n" {
		t.Fatalf("Get() result = %q, want canonical result", gotResult)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-1.index.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy task index should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks.lookup.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy task lookup should not exist, stat err = %v", err)
	}

	entry.UpdatedAt = time.Unix(30, 0)
	entry.Result = map[string]any{
		"stdout": "hello again\n",
		"stderr": "warn again\n",
		"result": "hello again\nwarn again\n",
		"state":  "completed",
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert(repeated final) error = %v", err)
	}
	got, err = store.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get(after repeated upsert) error = %v", err)
	}
	if gotResult, _ := got.Result["result"].(string); gotResult != "hello again\nwarn again\n" {
		t.Fatalf("Get(after repeated upsert) result = %q, want canonical result", gotResult)
	}
}

func TestTaskStoreUpsertCompletedTaskDropsWhitespaceOnlyStreams(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	entry := &task.Entry{
		TaskID:    "task-blank",
		Kind:      task.KindCommand,
		Session:   taskSessionRef("sess-blank"),
		State:     task.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(10, 0),
		UpdatedAt: time.Unix(20, 0),
		Result: map[string]any{
			"stdout": "\n\n",
			"stderr": "   ",
			"state":  "completed",
		},
		Terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  "exec-blank",
			TerminalID: "term-blank",
		},
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	listed, err := store.ListSession(context.Background(), taskSessionRef("sess-blank"))
	if err != nil {
		t.Fatalf("ListSession() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("len(listed) = %d, want 1", len(listed))
	}
	if _, ok := listed[0].Result["stdout"]; ok {
		t.Fatalf("ListSession result unexpectedly contains stdout: %#v", listed[0].Result)
	}
	if _, ok := listed[0].Result["stderr"]; ok {
		t.Fatalf("ListSession result unexpectedly contains stderr: %#v", listed[0].Result)
	}
}

func TestTaskStoreUpsertRunningTaskKeepsSQLiteOnly(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	entry := &task.Entry{
		TaskID:  "task-running",
		Kind:    task.KindCommand,
		Session: taskSessionRef("sess-2"),
		Title:   "RUN_COMMAND sleep 1",
		State:   task.StateRunning,
		Running: true,
		Result: map[string]any{
			"task_id":        "task-running",
			"state":          "running",
			"latest_output":  "live output\n",
			"output_preview": "preview\n",
			"output":         "stream output\n",
			"text":           "stream text\n",
		},
		Terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  "exec-running",
			TerminalID: "term-running",
		},
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-2.index.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy task index should not exist for running task, stat err = %v", err)
	}
	got, err := store.Get(context.Background(), "task-running")
	if err != nil {
		t.Fatalf("Get(running) error = %v", err)
	}
	assertNoTransientTaskResultKeys(t, got.Result)
	if gotState, _ := got.Result["state"].(string); gotState != "running" {
		t.Fatalf("Get(running) state = %q, want running", gotState)
	}
}

func TestTaskStoreGetIgnoresLegacyTaskJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	entry := &task.Entry{
		TaskID:    "task-target",
		Kind:      task.KindCommand,
		Session:   taskSessionRef("session-target"),
		Title:     "RUN_COMMAND echo target",
		State:     task.StateCompleted,
		Running:   false,
		UpdatedAt: time.Unix(20, 0),
		Result:    map[string]any{"state": "completed"},
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "aaa-decoy.index.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile(decoy) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks.lookup.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile(lookup) error = %v", err)
	}

	got, err := store.Get(context.Background(), "task-target")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Session.SessionID != "session-target" {
		t.Fatalf("Get() session = %q, want session-target", got.Session.SessionID)
	}
}

func TestTaskStoreNilReceiverReturnsErrors(t *testing.T) {
	t.Parallel()

	var store *TaskStore
	if err := store.Upsert(context.Background(), &task.Entry{TaskID: "task-1"}); err == nil {
		t.Fatal("Upsert(nil receiver) succeeded, want error")
	}
	if _, err := store.Get(context.Background(), "task-1"); err == nil {
		t.Fatal("Get(nil receiver) succeeded, want error")
	}
	if _, err := store.ListSession(context.Background(), taskSessionRef("sess-1")); err == nil {
		t.Fatal("ListSession(nil receiver) succeeded, want error")
	}
	if _, err := store.GetSessionTaskByHandle(context.Background(), taskSessionRef("sess-1"), "reviewer"); err == nil {
		t.Fatal("GetSessionTaskByHandle(nil receiver) succeeded, want error")
	}
}

func TestTaskStoreGetSessionTaskByHandleUsesIndexedHandle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	ref := taskSessionRef("sess-handle")
	if err := store.Upsert(context.Background(), &task.Entry{
		TaskID:  "task-command-decoy",
		Kind:    task.KindCommand,
		Session: ref,
		State:   task.StateCompleted,
		Handle:  "command-decoy",
		Result:  map[string]any{"state": "completed"},
	}); err != nil {
		t.Fatalf("Upsert(command decoy) error = %v", err)
	}
	if err := store.Upsert(context.Background(), &task.Entry{
		TaskID:  "task-other-session",
		Kind:    task.KindSubagent,
		Session: taskSessionRef("sess-other"),
		State:   task.StateCompleted,
		Handle:  "reviewer",
		Result:  map[string]any{"state": "completed"},
	}); err != nil {
		t.Fatalf("Upsert(other session) error = %v", err)
	}
	if err := store.Upsert(context.Background(), &task.Entry{
		TaskID:  "task-target",
		Kind:    task.KindSubagent,
		Session: ref,
		State:   task.StateCompleted,
		Handle:  "Reviewer",
		Result:  map[string]any{"state": "completed"},
	}); err != nil {
		t.Fatalf("Upsert(target) error = %v", err)
	}

	got, err := store.GetSessionTaskByHandle(context.Background(), ref, "@REVIEWER")
	if err != nil {
		t.Fatalf("GetSessionTaskByHandle() error = %v", err)
	}
	if got.TaskID != "task-target" {
		t.Fatalf("GetSessionTaskByHandle() task = %q, want task-target", got.TaskID)
	}
}

func TestTaskStoreScopedReadsVerifyOwningSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ref := taskSessionRef("session")
	ref.WorkspaceKey = "workspace"
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	if err := store.Upsert(t.Context(), &task.Entry{
		TaskID: "task-original", Session: ref, Handle: "reviewer", State: task.StateCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	reopened := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))

	for _, tt := range []struct {
		name string
		ref  session.SessionRef
	}{
		{"app", session.SessionRef{SessionID: ref.SessionID, AppName: "another-app"}},
		{"user", session.SessionRef{SessionID: ref.SessionID, UserID: "another-user"}},
		{"workspace", session.SessionRef{SessionID: ref.SessionID, WorkspaceKey: "another-workspace"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			list, err := reopened.ListSession(t.Context(), tt.ref)
			if err != nil || len(list) != 0 {
				t.Errorf("ListSession = %v, %v, want no entries in mismatched scope", list, err)
			}
			states, err := reopened.ReadTaskStates(t.Context(), tt.ref, []string{"task-original"})
			if err != nil || len(states) != 0 {
				t.Errorf("ReadTaskStates = %v, %v, want no entries in mismatched scope", states, err)
			}
			entry, err := reopened.GetSessionTaskByHandle(t.Context(), tt.ref, "reviewer")
			if err == nil || entry != nil {
				t.Errorf("GetSessionTaskByHandle = %v, %v, want not found", entry, err)
			}
		})
	}
	for _, scope := range []session.SessionRef{ref, {SessionID: ref.SessionID}} {
		entries, err := reopened.ListSession(t.Context(), scope)
		if err != nil || len(entries) != 1 || entries[0].TaskID != "task-original" {
			t.Errorf("ListSession = %v, %v, want original task", entries, err)
		}
		states, err := reopened.ReadTaskStates(t.Context(), scope, []string{"task-original"})
		if err != nil || len(states) != 1 || states[0].TaskID != "task-original" {
			t.Errorf("ReadTaskStates = %v, %v, want original task", states, err)
		}
		entry, err := reopened.GetSessionTaskByHandle(t.Context(), scope, "reviewer")
		if err != nil || entry == nil || entry.TaskID != "task-original" {
			t.Errorf("GetSessionTaskByHandle = %v, %v, want original task", entry, err)
		}
	}
}

func TestTaskStoreRejectsDuplicateSessionHandle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	ref := taskSessionRef("sess-ambiguous")
	for index, id := range []string{"task-a", "task-b"} {
		kind := task.KindSubagent
		if index == 1 {
			kind = task.KindCommand
		}
		err := store.Upsert(context.Background(), &task.Entry{
			TaskID:  id,
			Handle:  "reviewer",
			Kind:    kind,
			Session: ref,
			State:   task.StateCompleted,
			Result:  map[string]any{"state": "completed"},
		})
		if index == 0 && err != nil {
			t.Fatalf("Upsert(%s) error = %v", id, err)
		}
		if index == 1 {
			var conflict *task.HandleConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("Upsert(%s) error = %v, want HandleConflictError", id, err)
			}
		}
	}
}

func TestTaskStoreConcurrentUpsertKeepsAllTasks(t *testing.T) {
	t.Parallel()

	// This stress test proves SQLite task serialization and complete logical
	// readback; task-index crash durability has separate focused coverage.
	root := t.TempDir()
	store := NewTaskStore(newLogicalTestStore(t, Config{RootDir: root, Clock: fixedTaskClock}))
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry := &task.Entry{
				TaskID:    "task-concurrent-" + time.Unix(int64(i), 0).Format("150405"),
				Kind:      task.KindCommand,
				Session:   taskSessionRef("sess-concurrent"),
				Title:     "RUN_COMMAND concurrent",
				State:     task.StateCompleted,
				Running:   false,
				UpdatedAt: time.Unix(int64(i), 0),
				Result: map[string]any{
					"stdout": "out\n",
					"state":  "completed",
				},
			}
			if err := store.Upsert(context.Background(), entry); err != nil {
				t.Errorf("Upsert(%d) error = %v", i, err)
			}
		}()
	}
	wg.Wait()

	listed, err := store.ListSession(context.Background(), taskSessionRef("sess-concurrent"))
	if err != nil {
		t.Fatalf("ListSession() error = %v", err)
	}
	if len(listed) != 24 {
		t.Fatalf("len(listed) = %d, want 24", len(listed))
	}
}

func taskSessionRef(sessionID string) session.SessionRef {
	return session.SessionRef{AppName: "caelis", UserID: "user-1", SessionID: sessionID}
}

func fixedTaskClock() time.Time { return time.Unix(100, 0).UTC() }

func assertNoTransientTaskResultKeys(t *testing.T, result map[string]any) {
	t.Helper()
	sanitized := task.SanitizeResultForPersistence(result, task.ResultPersistenceCanonical)
	if !reflect.DeepEqual(sanitized, result) {
		t.Fatalf("task result still carries transient keys: sanitized = %#v, want %#v", sanitized, result)
	}
}

func TestTaskStateReadsSelectSessionAndTaskWithoutPayloads(t *testing.T) {
	store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
	ref := taskSessionRef("state-session")
	for _, entry := range []*task.Entry{
		{TaskID: "completed", Session: ref, State: task.StateCompleted},
		{TaskID: "steering", Session: ref, State: task.StateRunning, Metadata: map[string]any{"supports_steering": true}},
		{TaskID: "uncertain", Session: ref, State: task.StateUnknownOutcome, Metadata: map[string]any{"supports_steering": true}},
		{TaskID: "foreign", Session: taskSessionRef("other"), State: task.StateRunning},
	} {
		entry.Kind = task.KindSubagent
		entry.Spec = map[string]any{"prompt": strings.Repeat("spec", 4096)}
		entry.Result = map[string]any{"final_message": strings.Repeat("result", 4096)}
		if err := store.Upsert(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.ReadTaskStates(t.Context(), ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	full, err := store.ListSession(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]task.StateSnapshot, 0, len(full))
	for _, entry := range full {
		steering, _ := entry.Metadata["supports_steering"].(bool)
		want = append(want, task.StateSnapshot{TaskID: entry.TaskID, Revision: entry.Revision, State: entry.State, SupportsSteering: steering})
	}
	if !reflect.DeepEqual(all, want) || len(all) != 3 {
		t.Fatalf("state-only projection differs: got %+v, want %+v", all, want)
	}
	selected, err := store.ReadTaskStates(t.Context(), ref, []string{" steering ", "foreign", "absent"})
	if err != nil || len(selected) != 1 || selected[0].TaskID != "steering" || !selected[0].SupportsSteering {
		t.Fatalf("selected states = %+v, %v", selected, err)
	}
	current, err := store.Get(t.Context(), "steering")
	if err != nil {
		t.Fatal(err)
	}
	current.State, current.Metadata["supports_steering"] = task.StateUnknownOutcome, false
	updated, err := store.Put(t.Context(), task.PutRequest{Entry: current, ExpectedRevision: current.Revision})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.ReadTaskStates(t.Context(), ref, []string{"steering"})
	if err != nil || len(fresh) != 1 || fresh[0].State != task.StateUnknownOutcome || fresh[0].Revision != updated.Revision || fresh[0].SupportsSteering {
		t.Fatalf("stale task state = %+v, %v", fresh, err)
	}
	// Caller mutation cannot corrupt any later observation.
	fresh[0].State = task.StateCompleted
	again, err := store.ReadTaskStates(t.Context(), ref, []string{"steering"})
	if err != nil || again[0].State != task.StateUnknownOutcome {
		t.Fatalf("caller changed durable state: %+v, %v", again, err)
	}
}

func TestTaskStateSteeringMatchesFullMetadataDecoder(t *testing.T) {
	for _, metadata := range []string{
		`null`, ``, " \n\t ", "\u00a0{\"supports_steering\":true}\u00a0", `{}`, `{"supports_steering":true}`,
		`{"supports_steering":false}`, `{"supports_steering":"true"}`, `{"supports_steering":1}`,
		`{"supports_steering":true,"supports_steering":false}`, `{"supports_steering":false,"supports_steering":true}`,
		`{"\u0073upports_steering":true}`, `{"SUPPORTS_STEERING":true}`,
		`{"supports_steering":{}}`, `{"supports_steering":1e10000}`, `{"supports_steering":true,"unrelated":1e10000}`, `{"unrelated":1e10000}`, `[]`, `true`, `"text"`, `{"supports_steering":`,
	} {
		t.Run(metadata, func(t *testing.T) {
			store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
			ref := taskSessionRef("metadata")
			if err := store.Upsert(t.Context(), &task.Entry{TaskID: "target", Kind: task.KindSubagent, Session: ref, State: task.StateRunning}); err != nil {
				t.Fatal(err)
			}
			db, err := store.store.openTaskIndex()
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`UPDATE tasks SET metadata_json=? WHERE task_id='target'`, metadata)
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
			full, fullErr := store.ListSession(t.Context(), ref)
			states, stateErr := store.ReadTaskStates(t.Context(), ref, nil)
			if (fullErr != nil) != (stateErr != nil) {
				t.Fatalf("full error %v, state-only error %v", fullErr, stateErr)
			}
			if fullErr == nil {
				want, _ := full[0].Metadata["supports_steering"].(bool)
				if len(states) != 1 || states[0].SupportsSteering != want {
					t.Fatalf("steering = %+v, want %v", states, want)
				}
			}
		})
	}
}

func TestTaskStateReadHonorsCanceledStoreAcquisition(t *testing.T) {
	store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
	if err := store.store.mu.LockContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer store.store.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := store.ReadTaskStates(ctx, taskSessionRef("canceled"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v", err)
	}
	var missing *TaskStore
	if _, err := missing.ReadTaskStates(t.Context(), taskSessionRef("missing"), nil); err == nil {
		t.Fatal("nil TaskStore returned a successful observation")
	}
}

func TestTaskStoreSubagentCancellationPreservesIdentityAndGeneration(t *testing.T) {
	for _, mutation := range []string{"", "identity", "generation", "activity", "spec", "retired_result", "cursor", "missing_journal", "foreign_session", "stale_revision", "reopen"} {
		t.Run(mutation, func(t *testing.T) {
			store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
			current, err := store.Put(t.Context(), task.PutRequest{Entry: &task.Entry{
				TaskID: "child", Kind: task.KindSubagent, Session: taskSessionRef("parent"), State: task.StateRunning, Running: true,
				Spec:     map[string]any{"turn_seq": int64(1), "agent_id": "agent", "session_id": "child-session", "prompt": "retained"},
				Metadata: map[string]any{"child_activity_id": "first", "child_activity_generation": int64(1)},
			}})
			if err != nil {
				t.Fatal(err)
			}
			current, err = store.Get(t.Context(), current.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			next := task.CloneEntry(current)
			next.State = task.StateUnknownOutcome
			next.Spec["cancel_phase"], next.Spec["cancel_turn_seq"] = "subagent_cancel_claimed", int64(1)
			next.Metadata["cancel_phase"], next.Metadata["cancel_turn_seq"] = "subagent_cancel_claimed", int64(1)
			expected := current.Revision
			switch mutation {
			case "identity":
				next.Spec["session_id"] = "other-child"
			case "generation":
				next.Spec["turn_seq"] = int64(2)
			case "activity":
				next.Metadata["child_activity_id"] = "other-activity"
			case "spec":
				next.Spec["prompt"] = "replacement"
			case "retired_result":
				next.Spec["spawn_result"] = map[string]any{"result": "replacement"}
			case "cursor":
				next.EventCursor++
			case "missing_journal":
				delete(next.Metadata, "cancel_phase")
			case "foreign_session":
				next.Session.SessionID = "other-parent"
			case "stale_revision":
				expected--
			case "reopen":
				next.State = task.StateRunning
			}
			ctx := session.ContextWithControlMutation(t.Context(), session.ControlMutationPurposeSubagentCancellation)
			_, err = store.Put(ctx, task.PutRequest{Entry: next, ExpectedRevision: expected})
			if mutation == "" {
				if err != nil {
					t.Fatalf("cancel claim rejected: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("cancellation changed %s", mutation)
				}
				got, loadErr := store.Get(t.Context(), current.TaskID)
				if loadErr != nil || !reflect.DeepEqual(got, current) {
					t.Fatalf("rejected mutation changed Task: %#v, %v", got, loadErr)
				}
			}
		})
	}
}
