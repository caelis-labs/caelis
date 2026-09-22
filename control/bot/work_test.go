package bot

import (
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestWorkAuthorityPersistsAndRejectsForgedSources(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id := Identity("alice", "create")
	parts := []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: "aW1n", FileName: "report.png"}}
	source, err := store.RecordRequest(ctx, "alice", id, "", "prompt", "digest", "prepare a report", parts...)
	if err != nil {
		t.Fatal(err)
	}
	work := Work{ID: WorkID(id, "delegate"), BotID: id, PrincipalID: "alice", SourceID: source.ID, CreationOperationID: "delegate", CreationDigest: "full-request-digest"}
	if _, err := store.ReserveWork(ctx, work); err == nil {
		t.Fatal("unconfirmed request authorized work")
	}
	execution := Execution{InstanceID: "instance", SessionID: "chat", HandleID: "handle", RunID: "run", TurnID: "turn"}
	if err := store.BindRequest(ctx, source, execution); err != nil {
		t.Fatal(err)
	}
	fresh, err := store.ReserveWork(ctx, work)
	if err != nil || !fresh {
		t.Fatalf("reserve: %v %v", fresh, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restoredSource, err := store.RequestForOperation(ctx, "alice", id, "prompt")
	if err != nil || !reflect.DeepEqual(restoredSource.ContentParts, parts) || restoredSource.Execution != execution {
		t.Fatalf("source image/target recovery: %+v %v", restoredSource, err)
	}
	fresh, err = store.ReserveWork(ctx, work)
	if err != nil || fresh {
		t.Fatalf("reopened retry: %v %v", fresh, err)
	}
	if _, err := store.GetWork(ctx, "bob", id, work.ID); errorcode.CodeOf(err) != errorcode.PermissionDenied {
		t.Fatalf("cross principal read: %v", err)
	}
	if _, err := store.GetWork(ctx, "alice", Identity("alice", "other-bot"), work.ID); errorcode.CodeOf(err) != errorcode.PermissionDenied {
		t.Fatalf("cross Bot read: %v", err)
	}
	changed := work
	changed.CreationDigest = "different"
	if _, err := store.ReserveWork(ctx, changed); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("conflicting creation: %v", err)
	}
	if _, err := store.RecordRequest(ctx, "alice", id, "", "prompt", "different", "injected"); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("conflicting source: %v", err)
	}
	if _, err := store.Request(ctx, "alice", id, "note-generated-source"); err == nil {
		t.Fatal("invented source accepted")
	}
	execution.RunID = "new-run"
	if err := store.BindRequest(ctx, source, execution); errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("source rebound: %v", err)
	}
}

func TestWorkFilesCannotReachBotFilesOrOtherWork(t *testing.T) {
	store := t.TempDir()
	id := Identity("alice", "bot")
	first, err := NewWorkFiles(store, id, WorkID(id, "one"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewWorkFiles(store, id, WorkID(id, "two"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := second.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := second.FileSystem().WriteFile(NotebookIndex, []byte("private second work"), 0600); err != nil {
		t.Fatal(err)
	}
	main, err := NewFiles(store, id)
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	if err := main.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, _ := first.FileSystem().Getwd()
	b, _ := second.FileSystem().Getwd()
	if a == b {
		t.Fatal("workspaces shared")
	}
	if _, err := first.FileSystem().Open(filepath.Join(b, NotebookIndex)); err == nil {
		t.Fatal("cross-work read accepted")
	}
	if _, err := first.FileSystem().Open("../../../../files/index.md"); err == nil {
		t.Fatal("Bot file escape accepted")
	}
}

func TestWorkUnknownOperationNeverBecomesFreshAfterRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id := Identity("owner", "bot")
	intent := WorkOperation{PrincipalID: "owner", BotID: id, WorkID: WorkID(id, "create"), OperationID: "create", SourceID: "source", Digest: "all-request-fields"}
	if _, fresh, err := store.ReserveOperation(ctx, intent); err != nil || !fresh {
		t.Fatalf("reserve: %v %v", fresh, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receipt, fresh, err := store.ReserveOperation(ctx, intent)
	if err != nil || fresh || receipt.OperationID != intent.OperationID {
		t.Fatalf("uncertain intent replay: %+v %v %v", receipt, fresh, err)
	}
	recovered, err := store.Operation(ctx, intent.PrincipalID, intent.BotID, intent.OperationID)
	if err != nil || recovered.Outcome != "unknown" {
		t.Fatalf("unknown recovery: %+v %v", recovered, err)
	}
	replacement := intent
	replacement.OperationID, replacement.WorkID, replacement.Digest = "replacement", WorkID(id, "replacement"), "reworded assignment"
	if _, fresh, err := store.ReserveOperation(ctx, replacement); fresh || errorcode.CodeOf(err) != errorcode.UnknownOutcome {
		t.Fatalf("new ID bypassed unknown source operation: %v %v", fresh, err)
	}
	intent.Digest = "changed"
	if _, _, err := store.ReserveOperation(ctx, intent); err == nil {
		t.Fatal("changed request accepted")
	}
}

func TestCompletionAcknowledgementSuppressesUnclaimedReport(t *testing.T) {
	ctx := t.Context()
	store, err := OpenWorkStore(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := Completion{ID: "completion", PrincipalID: "owner", BotID: Identity("owner", "bot"), ReportState: "pending"}
	if _, err := store.db.Put(ctx, "completion", item.ID, item); err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeCompletion(ctx, item.PrincipalID, item.BotID, item.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimCompletion(ctx, item); err != nil || claimed {
		t.Fatalf("read completion caused report: %v %v", claimed, err)
	}
	notices, err := store.Completions(ctx, item.PrincipalID, item.BotID)
	if err != nil || len(notices) != 1 || !notices[0].Acknowledged || notices[0].ReportState != "suppressed" {
		t.Fatalf("acknowledgement: %+v %v", notices, err)
	}
}

func TestCompletionClaimCrashAndExplicitStopAreDurable(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	item := Completion{ID: "completion", PrincipalID: "owner", BotID: Identity("owner", "bot"), ReportState: "pending"}
	if _, err := store.db.Put(ctx, "completion", item.ID, item); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var claims atomic.Int64
	for range 16 {
		wg.Go(func() {
			ok, err := store.ClaimCompletion(ctx, item)
			if err != nil {
				t.Error(err)
			}
			if ok {
				claims.Add(1)
			}
		})
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("report claimed %d times", claims.Load())
	}
	if err := store.PauseReports(ctx, item.PrincipalID, item.BotID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenWorkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if ok, err := store.ClaimCompletion(ctx, item); err != nil || ok {
		t.Fatalf("unknown claim replayed: %v %v", ok, err)
	}
	paused, err := store.ReportsPaused(ctx, item.PrincipalID, item.BotID)
	if err != nil || !paused {
		t.Fatalf("explicit stop lost: %v %v", paused, err)
	}
	notices, err := store.Completions(ctx, item.PrincipalID, item.BotID)
	if err != nil || len(notices) != 1 || notices[0].ReportState != "claimed" || notices[0].ReportExecution != (Execution{}) {
		t.Fatalf("unknown report receipt: %+v %v", notices, err)
	}
}
