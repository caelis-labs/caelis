package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func newFileTaskStoreForTest(t testing.TB) taskapi.Store {
	t.Helper()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	return sessionfile.NewTaskStore(store)
}

func TestPersistTaskEntryConsumesExactCommittedCASResult(t *testing.T) {
	t.Parallel()
	store := &committedResultTaskStore{}
	runtime := &taskRuntime{store: store, tasks: map[string]*commandTask{}, subagents: map[string]*subagentTask{}}
	entry := &taskapi.Entry{
		TaskID: "committed-task", Revision: 4, Kind: taskapi.KindCommand,
		Session: session.SessionRef{SessionID: "committed-session"}, State: taskapi.StateRunning, Running: true,
	}
	if err := runtime.persistTaskEntry(context.Background(), entry); err != nil {
		t.Fatalf("persistTaskEntry() error = %v, want committed result consumed", err)
	}
	if entry.Revision != 5 {
		t.Fatalf("entry revision = %d, want exact committed revision 5", entry.Revision)
	}
}

func TestPersistTaskEntryNotifiesRuntimeLifetimeOnlyAfterTerminalCommit(t *testing.T) {
	t.Parallel()
	store := &committedResultTaskStore{}
	var notified []session.SessionRef
	var committed []*taskapi.Entry
	runtime := &taskRuntime{
		store: store, tasks: map[string]*commandTask{}, subagents: map[string]*subagentTask{},
		activityChanged: func(ref session.SessionRef) { notified = append(notified, ref) },
		taskCommitted:   func(entry *taskapi.Entry) { committed = append(committed, entry) },
	}
	entry := &taskapi.Entry{
		TaskID: "lifetime-task", Kind: taskapi.KindSubagent,
		Session: session.SessionRef{SessionID: "lifetime-session"}, State: taskapi.StateRunning, Running: true,
	}
	if err := runtime.persistTaskEntry(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 0 {
		t.Fatalf("running Task notified Runtime lifetime: %#v", notified)
	}
	if len(committed) != 1 || committed[0].Revision != 1 || !committed[0].Running {
		t.Fatalf("running Task commit notifications = %#v, want cloned committed entry", committed)
	}
	entry.State = taskapi.StateCompleted
	entry.Running = false
	if err := runtime.persistTaskEntry(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 1 || notified[0].SessionID != "lifetime-session" {
		t.Fatalf("terminal Task lifetime notifications = %#v, want one owning Session", notified)
	}
	if len(committed) != 2 || committed[1].Revision != 2 || committed[1].Running || committed[1].State != taskapi.StateCompleted {
		t.Fatalf("terminal Task commit notifications = %#v, want second cloned committed entry", committed)
	}
}

type committedResultTaskStore struct{}

func (*committedResultTaskStore) Upsert(context.Context, *taskapi.Entry) error { return nil }
func (*committedResultTaskStore) Get(context.Context, string) (*taskapi.Entry, error) {
	return nil, errors.New("unexpected reload")
}
func (*committedResultTaskStore) ListSession(context.Context, session.SessionRef) ([]*taskapi.Entry, error) {
	return nil, nil
}
func (*committedResultTaskStore) GetSessionTaskByHandle(context.Context, session.SessionRef, string) (*taskapi.Entry, error) {
	return nil, errors.New("not found")
}
func (*committedResultTaskStore) Put(_ context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	persisted := taskapi.SanitizeEntryForPersistence(req.Entry, taskapi.ResultPersistenceCanonical)
	persisted.Revision = req.ExpectedRevision + 1
	return persisted, &session.CommittedError{Err: errors.New("report failed after commit")}
}
