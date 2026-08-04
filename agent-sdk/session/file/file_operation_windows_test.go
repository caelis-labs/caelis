//go:build windows

package file

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"golang.org/x/sys/windows"
)

func TestWindowsReplaceFileRecoversAfterDeleteShareHandleCloses(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "replacement.tmp")
	to := filepath.Join(dir, "document.json")
	if err := os.WriteFile(from, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	if err := os.WriteFile(to, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	handle := openWithoutDeleteSharing(t, to)
	closed := closeHandleAfter(handle, 60*time.Millisecond)

	if err := replaceFile(context.Background(), nil, fileOperationReplaceDocument, from, to); err != nil {
		t.Fatalf("replaceFile() error = %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("CloseHandle() error = %v", err)
	}
	data, err := os.ReadFile(to)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}
	if string(data) != "new" {
		t.Fatalf("target = %q, want replacement", data)
	}
}

func TestWindowsRemoveFileRecoversAfterDeleteShareHandleCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.txn.json")
	if err := os.WriteFile(path, []byte("committed"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	handle := openWithoutDeleteSharing(t, path)
	closed := closeHandleAfter(handle, 60*time.Millisecond)

	if err := removeFile(context.Background(), nil, fileOperationRemoveTransaction, path); err != nil {
		t.Fatalf("removeFile() error = %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("CloseHandle() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(removed file) error = %v, want not exist", err)
	}
}

func TestWindowsReplaceFileCancellationPreservesBothFiles(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "replacement.tmp")
	to := filepath.Join(dir, "document.json")
	if err := os.WriteFile(from, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	if err := os.WriteFile(to, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	handle := openWithoutDeleteSharing(t, to)
	defer func() { _ = windows.CloseHandle(handle) }()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := replaceFile(ctx, nil, fileOperationReplaceDocument, from, to)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("replaceFile() error = %v, want deadline exceeded", err)
	}
	for path, want := range map[string]string{from: "new", to: "old"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("ReadFile(%q) error = %v", filepath.Base(path), readErr)
		}
		if string(data) != want {
			t.Fatalf("ReadFile(%q) = %q, want %q", filepath.Base(path), data, want)
		}
	}
}

func TestWindowsAccessDeniedRetryRequiresAccessibleFiles(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "replacement.tmp")
	to := filepath.Join(dir, "document.json")
	if err := os.WriteFile(from, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	if err := os.WriteFile(to, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	policy, retry := windowsFileRetryPolicyFor(fileOperationReplaceDocument, from, to, windows.ERROR_ACCESS_DENIED)
	if !retry || policy.budget != windowsAccessDeniedRetryBudget || policy.budget >= windowsSharingRetryBudget {
		t.Fatalf("access-denied policy = %#v, retry %t; want shorter bounded retry", policy, retry)
	}

	attempts := 0
	err := retryWindowsFileOperation(context.Background(), nil, fileOperationReplaceDocument, from, to, false, func() error {
		attempts++
		if attempts == 1 {
			return windows.ERROR_ACCESS_DENIED
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("eligible access-denied retry = attempts %d, error %v; want recovery on second attempt", attempts, err)
	}

	attempts = 0
	err = retryWindowsFileOperation(context.Background(), nil, fileOperationReplaceDocument, from, filepath.Join(dir, "missing.json"), false, func() error {
		attempts++
		return windows.ERROR_ACCESS_DENIED
	})
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || attempts != 1 {
		t.Fatalf("ineligible access-denied retry = attempts %d, error %v; want one immediate failure", attempts, err)
	}
	if got := display.UserVisibleError(err); got != "Caelis could not update local session storage." || strings.Contains(got, dir) {
		t.Fatalf("user-visible file error = %q, want path-free summary", got)
	}
}

func TestWindowsPathClassDistinguishesUNCAndExtendedLocalPaths(t *testing.T) {
	tests := map[string]string{
		`C:\store\sessions`:             "local",
		`\\?\C:\store\sessions`:         "local",
		`\\.\C:\store\sessions`:         "local",
		`\\server\share\sessions`:       "unc",
		`\\?\UNC\server\share\sessions`: "unc",
		`relative\sessions`:             "relative",
	}
	for path, want := range tests {
		if got := windowsPathClass(path); got != want {
			t.Errorf("windowsPathClass(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestWindowsCommittedDocumentReplaceKeepsWALAndRecoversOneModelFact(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, time.August, 1, 2, 3, 4, 0, time.UTC)
	store := NewStore(Config{RootDir: root, Clock: func() time.Time { return at }})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "session-windows-handle",
		Workspace: session.WorkspaceRef{Key: "workspace", CWD: `C:\workspace`},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatalf("resolveWritePath() error = %v", err)
	}
	handle := openWithoutDeleteSharing(t, documentPath)

	message := model.NewTextMessage(model.RoleUser, "canonical content")
	request := session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		ID: "event-windows-handle", Type: session.EventTypeUser, Message: &message,
	}}
	if _, err := store.AppendEvent(context.Background(), request); !session.IsCommitted(err) {
		_ = windows.CloseHandle(handle)
		t.Fatalf("AppendEvent() error = %v, want committed replace failure", err)
	}
	walPath := transactionPath(documentPath)
	if _, err := os.Stat(walPath); err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatalf("Stat(committed WAL) error = %v, want retained WAL", err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatalf("CloseHandle() error = %v", err)
	}

	reopened := NewStore(Config{RootDir: root, Clock: func() time.Time { return at }})
	loaded, err := reopened.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession(recovery) error = %v", err)
	}
	if loaded.Session.Revision != 1 || len(loaded.Events) != 1 || loaded.Events[0].ID != request.Event.ID {
		t.Fatalf("recovered Session/events = revision %d events %#v, want one canonical fact", loaded.Session.Revision, loaded.Events)
	}
	replayed, ok := session.ModelMessageOf(loaded.Events[0])
	if !ok || !reflect.DeepEqual(replayed, message) {
		t.Fatalf("recovered model context = %#v, want %#v", replayed, message)
	}
	if _, err := os.Stat(walPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(recovered WAL) error = %v, want removed", err)
	}
	if _, err := reopened.AppendEvent(context.Background(), request); err != nil {
		t.Fatalf("AppendEvent(idempotent retry) error = %v", err)
	}
	afterRetry, err := reopened.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("Events(after retry) error = %v", err)
	}
	if len(afterRetry) != 1 || afterRetry[0].ID != request.Event.ID {
		t.Fatalf("events after idempotent retry = %#v, want one fact", afterRetry)
	}
}

func TestWindowsCancellationAfterWALRemainsCommittedAndRecoverable(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, time.August, 2, 2, 3, 4, 0, time.UTC)
	store := NewStore(Config{RootDir: root, Clock: func() time.Time { return at }})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "session-windows-cancel",
		Workspace: session.WorkspaceRef{Key: "workspace", CWD: `C:\workspace`},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatalf("resolveWritePath() error = %v", err)
	}
	handle := openWithoutDeleteSharing(t, documentPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelScheduled := false
	canceled := make(chan struct{})
	store.transactionFault = func(phase string) error {
		if phase == "after_event_log" && !cancelScheduled {
			cancelScheduled = true
			go func() {
				time.Sleep(15 * time.Millisecond)
				cancel()
				close(canceled)
			}()
		}
		return nil
	}
	message := model.NewTextMessage(model.RoleUser, "canonical cancellation content")
	request := session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		ID: "event-windows-cancel", Type: session.EventTypeUser, Message: &message,
	}}
	_, appendErr := store.AppendEvent(ctx, request)
	if !cancelScheduled {
		_ = windows.CloseHandle(handle)
		t.Fatalf("AppendEvent() error = %v before post-WAL cancellation was scheduled", appendErr)
	}
	<-canceled
	if !session.IsCommitted(appendErr) || !errors.Is(appendErr, context.Canceled) {
		_ = windows.CloseHandle(handle)
		t.Fatalf("AppendEvent() error = %v, want committed cancellation", appendErr)
	}
	walPath := transactionPath(documentPath)
	if _, err := os.Stat(walPath); err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatalf("Stat(committed WAL) error = %v, want retained WAL", err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatalf("CloseHandle() error = %v", err)
	}

	reopened := NewStore(Config{RootDir: root, Clock: func() time.Time { return at }})
	loaded, err := reopened.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession(recovery) error = %v", err)
	}
	if loaded.Session.Revision != 1 || len(loaded.Events) != 1 || loaded.Events[0].ID != request.Event.ID {
		t.Fatalf("recovered Session/events = revision %d events %#v, want one canonical fact", loaded.Session.Revision, loaded.Events)
	}
	replayed, ok := session.ModelMessageOf(loaded.Events[0])
	if !ok || !reflect.DeepEqual(replayed, message) {
		t.Fatalf("recovered model context = %#v, want %#v", replayed, message)
	}
	if _, err := os.Stat(walPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(recovered WAL) error = %v, want removed", err)
	}
}

func TestWindowsFileDiagnosticOmitsPathsAndSessionIdentity(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	attempts := 0
	secretDir := filepath.Join(t.TempDir(), "private-workspace")
	secretPath := filepath.Join(secretDir, "session-secret.json")

	err := retryWindowsFileOperation(
		context.Background(),
		logger,
		fileOperationReplaceDocument,
		filepath.Join(secretDir, "source-secret.tmp"),
		secretPath,
		false,
		func() error {
			attempts++
			if attempts == 1 {
				return windows.ERROR_SHARING_VIOLATION
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("retryWindowsFileOperation() error = %v", err)
	}
	logLine := output.String()
	for _, forbidden := range []string{secretDir, "private-workspace", "session-secret", "source-secret"} {
		if strings.Contains(logLine, forbidden) {
			t.Fatalf("diagnostic leaked %q: %s", forbidden, logLine)
		}
	}
	for _, required := range []string{`"component":"session_file"`, `"operation":"replace_document"`, `"error_class":"sharing_violation"`, `"outcome":"recovered"`, `"attempts":2`} {
		if !strings.Contains(logLine, required) {
			t.Fatalf("diagnostic %s does not contain %s", logLine, required)
		}
	}
}

func openWithoutDeleteSharing(t *testing.T, path string) windows.Handle {
	t.Helper()
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString() error = %v", err)
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("CreateFile(no delete sharing) error = %v", err)
	}
	return handle
}

func closeHandleAfter(handle windows.Handle, delay time.Duration) <-chan error {
	closed := make(chan error, 1)
	go func() {
		time.Sleep(delay)
		closed <- windows.CloseHandle(handle)
	}()
	return closed
}
