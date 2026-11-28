package file

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestAttachmentPrunePreservesReferencesAndReappearance(t *testing.T) {
	start := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	now := start
	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Attachments: true, Clock: func() time.Time { return now }})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 32, 16))); err != nil {
		t.Fatal(err)
	}
	data := append(imageBytes.Bytes(), make([]byte, 64<<10)...)
	encoded := base64.StdEncoding.EncodeToString(data)
	event := func(id string) *session.Event {
		msg := model.NewMessage(model.RoleUser, model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: encoded}, "image/png", "screen"))
		return &session.Event{ID: id, IdempotencyKey: id, Type: session.EventTypeUser, Message: &msg}
	}
	appendOne := func(id string) model.AttachmentRef {
		t.Helper()
		out, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: event(id)})
		if err != nil {
			t.Fatal(err)
		}
		return out.Message.Parts[0].Media.Source.Attachment
	}
	old := appendOne("first")
	if again := appendOne("second"); old != again || old.Width != 32 || old.Height != 16 {
		t.Fatalf("attachment metadata/dedup: %+v and %+v", old, again)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".events.jsonl") {
			files = append(files, path)
		}
		return err
	})
	if err != nil || len(files) != 1 {
		t.Fatalf("event file: %v, %v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || bytes.Contains(raw, []byte(encoded)) || len(raw) > 4096 {
		t.Fatalf("JSONL bytes=%d err=%v", len(raw), err)
	}
	if _, err = store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef}); err != nil {
		t.Fatal(err)
	}
	preview, err := store.PruneAttachments(t.Context(), active.SessionID, start.Add(time.Second), false)
	if err != nil || preview.Files != 1 || preview.Bytes != int64(len(data)) {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err = store.PruneAttachments(t.Context(), active.SessionID, start.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	if again := appendOne("first"); old != again {
		t.Fatal("idempotent retry changed the attachment instance")
	}
	now = start.Add(2 * time.Second)
	fresh := appendOne("third")
	if fresh.Instance == old.Instance || fresh.Digest != old.Digest {
		t.Fatalf("old=%+v fresh=%+v", old, fresh)
	}
	if f, err := store.OpenAttachment(t.Context(), active.SessionID, old); !errors.Is(err, model.ErrAttachmentUnavailable) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("old ref reopened: %v", err)
	}
	f, err := store.OpenAttachment(t.Context(), active.SessionID, fresh)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := model.ReadAttachment(f, fresh)
	_ = f.Close()
	if err != nil || !bytes.Equal(loaded, data) {
		t.Fatal("new attachment bytes differ")
	}
	if f, err := store.OpenAttachment(t.Context(), "another-session", fresh); !errors.Is(err, model.ErrAttachmentUnavailable) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("cross-Session open=%v", err)
	}
}

func TestConcurrentAttachmentPublicationUsesOneInstance(t *testing.T) {
	store := NewStore(Config{RootDir: t.TempDir(), Attachments: true})
	var wg sync.WaitGroup
	ready := make(chan struct{})
	refs := make(chan model.AttachmentRef, 12)
	for range cap(refs) {
		wg.Go(func() {
			<-ready
			ref, err := store.StoreAttachment(t.Context(), "session", []byte("identical"))
			if err != nil {
				t.Error(err)
				return
			}
			refs <- ref
		})
	}
	close(ready)
	wg.Wait()
	close(refs)
	var first model.AttachmentRef
	for ref := range refs {
		if first.Instance != "" && ref != first {
			t.Error("concurrent publication created multiple instances")
		}
		first = ref
	}
	f, err := store.OpenAttachment(t.Context(), "session", first)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil || string(raw) != "identical" {
		t.Fatalf("body=%q err=%v", raw, err)
	}
	for range cap(refs) {
		wg.Go(func() {
			again, err := store.StoreAttachment(t.Context(), "session", []byte("identical"))
			if err != nil || again != first {
				t.Error("publication during reads changed the incarnation", err)
			}
			r, err := store.OpenAttachment(t.Context(), "session", first)
			if err != nil {
				t.Error(err)
				return
			}
			defer r.Close()
			got, err := model.ReadAttachment(r, first)
			if err != nil || string(got) != "identical" {
				t.Error("concurrent read did not get the original attachment", err)
			}
		})
	}
	wg.Wait()
}

func TestAttachmentWALStoresOnlyReferenceAndRecovers(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Attachments: true})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.findDocumentPath(active.SessionID, active.WorkspaceKey)
	if err != nil {
		t.Fatal(err)
	}
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := append(img.Bytes(), make([]byte, 32<<10)...)
	encoded := base64.StdEncoding.EncodeToString(data)
	msg := model.NewMessage(model.RoleUser, model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: encoded}, "image/png", "input"))
	store.transactionFault = func(phase string) error {
		if phase == "after_commit" {
			return errors.New("stop after WAL commit")
		}
		return nil
	}
	_, err = store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{ID: "with-image", Type: session.EventTypeUser, Message: &msg}})
	if !session.IsCommitted(err) {
		t.Fatalf("AppendEvent = %v, want recoverable committed WAL", err)
	}
	wal, err := os.ReadFile(transactionPath(path))
	if err != nil || len(wal) > 8<<10 || bytes.Contains(wal, []byte(encoded)) {
		t.Fatalf("WAL bytes=%d err=%v", len(wal), err)
	}
	reopened := NewStore(Config{RootDir: root, Attachments: true})
	event, err := reopened.EventReceipt(t.Context(), active.SessionRef, "with-image")
	if err != nil || event == nil {
		t.Fatalf("recovered receipt=%v err=%v", event, err)
	}
	ref := event.Message.Parts[0].Media.Source.Attachment
	file, err := reopened.OpenAttachment(t.Context(), active.SessionID, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := model.ReadAttachment(file, ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("replayed attachment content differs")
	}
	t.Logf("WAL=%d bytes, attachment=%d bytes", len(wal), len(data))
}
