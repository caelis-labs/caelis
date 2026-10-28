package file

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func fileIdleCheckpoint(t *testing.T) (*Store, session.AppendIdleCompactionCheckpointRequest, *time.Time) {
	t.Helper()
	now := time.Now()
	store := NewStore(Config{RootDir: t.TempDir(), Clock: func() time.Time { return now }})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "idle", UserID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	message := model.NewTextMessage(model.RoleAssistant, "complete source")
	event, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeAssistant, Message: &message}})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := session.CheckpointSourceStamp(loaded)
	if err != nil {
		t.Fatal(err)
	}
	window, err := store.ReleaseSessionFenceForIdleCompaction(t.Context(), session.NewIdleCompactionRelease(fence, session.IdleCompactionSource{JobID: "job", HostID: "host", ActivationID: "activation", TurnID: "turn", ThroughSeq: event.Seq, Stamp: stamp, Coverage: session.IdleCompactionCoverage{Revision: 1, EventCount: 1, EndSeq: event.Seq, EndID: event.ID}}))
	if err != nil {
		t.Fatal(err)
	}
	text := model.NewTextMessage(model.RoleUser, "CONTEXT CHECKPOINT")
	checkpoint := &session.Event{ID: "candidate", IdempotencyKey: "candidate", SessionID: active.SessionID, Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical, Message: &text, Meta: map[string]any{session.CheckpointMetadataKey: session.CheckpointDataValue(session.CheckpointData{ContractVersion: 1, Revision: 1, SourceEventCount: 1, SummarizedThroughSeq: event.Seq, SummarizedThroughID: event.ID})}}
	grant, err := store.IssueIdleCompactionGrant(t.Context(), session.IssueIdleCompactionGrantRequest{Window: window, Event: checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	return store, session.AppendIdleCompactionCheckpointRequest{Grant: grant, Event: checkpoint, ExpectedRevision: loaded.Session.Revision}, &now
}

func TestIdleCheckpointWALRecoveryKeepsConsumedGrantAndOriginalIdentity(t *testing.T) {
	store, req, now := fileIdleCheckpoint(t)
	store.transactionFault = func(phase string) error {
		if phase == "after_commit" {
			return errors.New("fixture: lost committed reply")
		}
		return nil
	}
	original, err := store.AppendIdleCompactionCheckpoint(t.Context(), req)
	if !session.IsCommitted(err) || original == nil {
		t.Fatalf("committed result=%v %v", original, err)
	}
	*now = now.Add(time.Hour)
	reopened := NewStore(Config{RootDir: store.rootDir, Clock: func() time.Time { return *now }})
	again, err := reopened.AppendIdleCompactionCheckpoint(t.Context(), req)
	if err != nil || again.ID != original.ID || again.Seq != original.Seq {
		t.Fatalf("original readback=%v %v", again, err)
	}
	loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: req.Grant.SessionRef()})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 2 {
		t.Fatalf("WAL replay duplicated checkpoint: %d", len(loaded.Events))
	}
}

func TestIdleCheckpointGrantExpiresDuringWALPreparation(t *testing.T) {
	store, req, now := fileIdleCheckpoint(t)
	store.durability.syncFile = func(file *os.File) error {
		if strings.Contains(file.Name(), transactionSuffix) {
			*now = now.Add(3 * time.Second)
		}
		return file.Sync()
	}
	if _, err := store.AppendIdleCompactionCheckpoint(t.Context(), req); !errors.Is(err, session.ErrIdleCompactionRevoked) {
		t.Fatalf("expired grant committed after WAL preparation: %v", err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: req.Grant.SessionRef()})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 1 {
		t.Fatal("expired checkpoint became durable")
	}
}
