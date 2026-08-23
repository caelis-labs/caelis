package credentialstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTripUsesOpaqueReferenceAndSecureFile(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openai", "openai@default")
	secret := "sk-super-secret"
	if ref == "" || strings.Contains(ref, secret) || strings.Contains(ref, "openai") {
		t.Fatalf("BuildReference() = %q", ref)
	}
	if err := store.Put(context.Background(), ref, secret); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("Get() = %q", got)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	var credentialEntries []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			credentialEntries = append(credentialEntries, entry)
		}
	}
	if len(credentialEntries) != 1 || strings.Contains(credentialEntries[0].Name(), secret) || strings.Contains(credentialEntries[0].Name(), ref) {
		t.Fatalf("credential files = %#v", entries)
	}
	info, err := os.Stat(filepath.Join(store.root, credentialEntries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o", info.Mode().Perm())
	}
}

func TestReplacementTransactionBlocksOtherStoreLookupUntilCommit(t *testing.T) {
	root := t.TempDir()
	writer, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openai", "openai@default")
	if err := writer.Put(context.Background(), ref, "old-secret"); err != nil {
		t.Fatal(err)
	}
	txn, err := writer.BeginReplacement(context.Background(), []Replacement{{
		Ref: ref, Source: Source{APIKey: "new-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = txn.Rollback() })

	type lookupResult struct {
		source Source
		err    error
	}
	result := make(chan lookupResult, 1)
	go func() {
		source, lookupErr := reader.LookupSource(context.Background(), ref)
		result <- lookupResult{source: source, err: lookupErr}
	}()
	select {
	case got := <-result:
		t.Fatalf("LookupSource returned before commit: %#v, %v", got.source, got.err)
	case <-time.After(75 * time.Millisecond):
	}
	if err := txn.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.source.APIKey != "new-secret" {
			t.Fatalf("LookupSource after commit = %#v, %v", got.source, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("LookupSource remained blocked after commit")
	}
}

func TestReplacementTransactionRollbackRestoresSourceBeforeUnblockingReader(t *testing.T) {
	root := t.TempDir()
	writer, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("anthropic", "anthropic@default")
	if err := writer.Put(context.Background(), ref, "accepted-secret"); err != nil {
		t.Fatal(err)
	}
	txn, err := writer.BeginReplacement(context.Background(), []Replacement{{
		Ref: ref, Source: Source{APIKey: "uncommitted-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan Source, 1)
	errResult := make(chan error, 1)
	go func() {
		source, lookupErr := reader.LookupSource(context.Background(), ref)
		result <- source
		errResult <- lookupErr
	}()
	select {
	case <-result:
		t.Fatal("LookupSource returned before rollback")
	case <-time.After(75 * time.Millisecond):
	}
	if err := txn.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case source := <-result:
		if lookupErr := <-errResult; lookupErr != nil || source.APIKey != "accepted-secret" {
			t.Fatalf("LookupSource after rollback = %#v, %v", source, lookupErr)
		}
	case <-time.After(time.Second):
		t.Fatal("LookupSource remained blocked after rollback")
	}
}

func TestRetirementTransactionCommitRemovesSourceBeforeUnblockingReader(t *testing.T) {
	root := t.TempDir()
	owner, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openrouter", "openrouter@default")
	if err := owner.Put(context.Background(), ref, "retired-secret"); err != nil {
		t.Fatal(err)
	}
	txn, err := owner.BeginRetirement(context.Background(), []string{ref}, 7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = txn.Rollback() })

	result := make(chan error, 1)
	go func() {
		_, lookupErr := reader.LookupSource(context.Background(), ref)
		result <- lookupErr
	}()
	select {
	case lookupErr := <-result:
		t.Fatalf("LookupSource returned before retirement commit: %v", lookupErr)
	case <-time.After(75 * time.Millisecond):
	}
	if err := txn.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case lookupErr := <-result:
		if !errors.Is(lookupErr, os.ErrNotExist) {
			t.Fatalf("LookupSource after retirement commit error = %v, want os.ErrNotExist", lookupErr)
		}
	case <-time.After(time.Second):
		t.Fatal("LookupSource remained blocked after retirement commit")
	}
}

func TestRetirementTransactionRollbackRestoresSourceBeforeUnblockingReader(t *testing.T) {
	root := t.TempDir()
	owner, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openrouter", "openrouter@default")
	if err := owner.Put(context.Background(), ref, "accepted-secret"); err != nil {
		t.Fatal(err)
	}
	txn, err := owner.BeginRetirement(context.Background(), []string{ref}, 7)
	if err != nil {
		t.Fatal(err)
	}

	type lookupResult struct {
		source Source
		err    error
	}
	result := make(chan lookupResult, 1)
	go func() {
		source, lookupErr := reader.LookupSource(context.Background(), ref)
		result <- lookupResult{source: source, err: lookupErr}
	}()
	select {
	case got := <-result:
		t.Fatalf("LookupSource returned before retirement rollback: %#v, %v", got.source, got.err)
	case <-time.After(75 * time.Millisecond):
	}
	if err := txn.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.source.APIKey != "accepted-secret" {
			t.Fatalf("LookupSource after retirement rollback = %#v, %v", got.source, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("LookupSource remained blocked after retirement rollback")
	}
}

func TestRetirementTransactionDeleteFailureRestoresEarlierSources(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows open-file deletion fault injection")
	}
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	refs := []string{
		BuildReference("openrouter", "retirement-failure-a"),
		BuildReference("openrouter", "retirement-failure-b"),
	}
	sort.Strings(refs)
	for index, ref := range refs {
		if err := store.Put(context.Background(), ref, fmt.Sprintf("accepted-secret-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := os.OpenFile(store.path(refs[1]), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginRetirement(context.Background(), refs, 7); err == nil {
		_ = blocked.Close()
		t.Fatal("BeginRetirement() succeeded while one credential could not be deleted")
	}
	if err := blocked.Close(); err != nil {
		t.Fatal(err)
	}
	for index, ref := range refs {
		want := fmt.Sprintf("accepted-secret-%d", index)
		if got, err := store.Get(context.Background(), ref); err != nil || got != want {
			t.Fatalf("Get(%q) after failed retirement = %q, %v; want %q", ref, got, err, want)
		}
	}
}

func TestRetirementRecoveryRemovesInterruptedReceiptTempUnderJournalLock(t *testing.T) {
	root := t.TempDir()
	owner, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureDir(owner.retirementRoot()); err != nil {
		t.Fatal(err)
	}
	tempPath := filepath.Join(owner.retirementRoot(), ".credential.interrupted.tmp")
	if err := os.WriteFile(tempPath, []byte("retired-secret-copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := owner.RecoverRetirements(context.Background(), func(context.Context) (map[string]struct{}, error) {
		t.Fatal("reachability callback called without a committed receipt")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted retirement temp error = %v, want os.ErrNotExist", err)
	}
}

func TestRetirementRecoveryWaitsForActiveReceiptWriter(t *testing.T) {
	root := t.TempDir()
	owner, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	contender, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openrouter", "active-retirement-writer")
	if err := owner.Put(context.Background(), ref, "retired-secret"); err != nil {
		t.Fatal(err)
	}
	txn, err := owner.BeginRetirement(context.Background(), []string{ref}, 11)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = txn.Rollback() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := contender.RecoverRetirements(ctx, func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{ref: {}}, nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RecoverRetirements() during active writer error = %v, want deadline exceeded", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestRetirementRecoveryReconcilesInterruptedTransaction(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reachable bool
		wantKey   string
	}{
		{name: "configuration-still-references-source", reachable: true, wantKey: "recoverable-secret"},
		{name: "configuration-committed-retirement", reachable: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			owner, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			ref := BuildReference("openrouter", tc.name)
			if err := owner.Put(context.Background(), ref, "recoverable-secret"); err != nil {
				t.Fatal(err)
			}
			txn, err := owner.BeginRetirement(context.Background(), []string{ref}, 41)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate process termination: platform locks disappear, while the
			// durable receipt and pre-CAS credential deletion remain.
			if err := txn.releaseLocks(); err != nil {
				t.Fatal(err)
			}

			restarted, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.RecoverRetirements(context.Background(), func(context.Context) (map[string]struct{}, error) {
				if tc.reachable {
					return map[string]struct{}{ref: {}}, nil
				}
				return map[string]struct{}{}, nil
			}); err != nil {
				t.Fatal(err)
			}
			got, getErr := restarted.Get(context.Background(), ref)
			if tc.wantKey == "" {
				if !errors.Is(getErr, os.ErrNotExist) {
					t.Fatalf("Get() after committed retirement = %q, %v; want os.ErrNotExist", got, getErr)
				}
			} else if getErr != nil || got != tc.wantKey {
				t.Fatalf("Get() after pre-CAS crash recovery = %q, %v; want %q", got, getErr, tc.wantKey)
			}
			assertNoRetirementReceipts(t, restarted)
		})
	}
}

func TestRetirementRecoveryDoesNotOverwriteNewerReplacement(t *testing.T) {
	root := t.TempDir()
	owner, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openrouter", "replaced-after-retirement-crash")
	if err := owner.Put(context.Background(), ref, "retired-secret"); err != nil {
		t.Fatal(err)
	}
	txn, err := owner.BeginRetirement(context.Background(), []string{ref}, 19)
	if err != nil {
		t.Fatal(err)
	}
	if err := txn.releaseLocks(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Put(context.Background(), ref, "newer-secret"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RecoverRetirements(context.Background(), func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{ref: {}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.Get(context.Background(), ref); err != nil || got != "newer-secret" {
		t.Fatalf("Get() after recovery = %q, %v; want newer replacement", got, err)
	}
	assertNoRetirementReceipts(t, restarted)
}

func assertNoRetirementReceipts(t *testing.T, store *Store) {
	t.Helper()
	entries, err := os.ReadDir(store.retirementRoot())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("retirement receipt remained after recovery: %s", entry.Name())
		}
	}
}

func TestReplacementTransactionMarksIncompleteRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory mode fault injection is Unix-only")
	}
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openai", "rollback-fault")
	txn, err := store.BeginReplacement(context.Background(), []Replacement{{
		Ref: ref, Source: Source{APIKey: "uncommitted-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(store.root, 0o700)
		_ = store.Delete(context.Background(), ref)
	})
	rollbackErr := txn.Rollback()
	if !errors.Is(rollbackErr, ErrRollbackIncomplete) {
		t.Fatalf("Rollback() error = %v, want ErrRollbackIncomplete", rollbackErr)
	}
}

func TestReferenceOperationsHonorCancellationWhileReplacementIsOpen(t *testing.T) {
	root := t.TempDir()
	owner, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	contender, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("deepseek", "deepseek@default")
	if err := owner.Put(context.Background(), ref, "accepted-secret"); err != nil {
		t.Fatal(err)
	}
	txn, err := owner.BeginReplacement(context.Background(), []Replacement{{
		Ref: ref, Source: Source{APIKey: "pending-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = txn.Rollback() })

	for name, operation := range map[string]func(context.Context) error{
		"lookup": func(ctx context.Context) error { _, lookupErr := contender.LookupSource(ctx, ref); return lookupErr },
		"put":    func(ctx context.Context) error { return contender.Put(ctx, ref, "other-secret") },
		"delete": func(ctx context.Context) error { return contender.Delete(ctx, ref) },
		"replacement": func(ctx context.Context) error {
			_, replacementErr := contender.BeginReplacement(ctx, []Replacement{{Ref: ref, Source: Source{APIKey: "other-secret"}}})
			return replacementErr
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := operation(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("operation error = %v, want deadline exceeded", err)
			}
		})
	}
	if err := txn.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got, err := contender.Get(context.Background(), ref); err != nil || got != "accepted-secret" {
		t.Fatalf("Get(after cancellation and rollback) = %q, %v", got, err)
	}
}

func TestReplacementBatchesUseSortedReferenceLocks(t *testing.T) {
	root := t.TempDir()
	first, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	refA := BuildReference("openai", "a")
	refB := BuildReference("openai", "b")
	firstTxn, err := first.BeginReplacement(context.Background(), []Replacement{
		{Ref: refB, Source: Source{APIKey: "first-b"}},
		{Ref: refA, Source: Source{APIKey: "first-a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstTxn.Rollback() })

	secondTxn := make(chan *ReplacementTransaction, 1)
	secondErr := make(chan error, 1)
	go func() {
		txn, beginErr := second.BeginReplacement(context.Background(), []Replacement{
			{Ref: refA, Source: Source{APIKey: "second-a"}},
			{Ref: refB, Source: Source{APIKey: "second-b"}},
		})
		secondTxn <- txn
		secondErr <- beginErr
	}()
	select {
	case <-secondTxn:
		t.Fatal("overlapping batch returned before first commit")
	case <-time.After(75 * time.Millisecond):
	}
	if err := firstTxn.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case txn := <-secondTxn:
		if err := <-secondErr; err != nil {
			t.Fatal(err)
		}
		if err := txn.Rollback(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("overlapping sorted batch deadlocked")
	}
	for ref, want := range map[string]string{refA: "first-a", refB: "first-b"} {
		if got, err := second.Get(context.Background(), ref); err != nil || got != want {
			t.Fatalf("Get(%q) after second rollback = %q, %v; want %q", ref, got, err, want)
		}
	}
}

func TestStoreMarksLegacyEnvironmentCredentialInvalidAndReplaceable(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("deepseek", "deepseek@default")
	if err := ensureDir(store.root); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":1,"ref":"` + ref + `","environment":"DEEPSEEK_API_KEY"}`)
	if err := os.WriteFile(store.path(ref), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), ref); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("Get(legacy environment credential) error = %v, want replaceable invalid credential", err)
	}
	if err := store.Put(context.Background(), ref, "replacement-secret"); err != nil {
		t.Fatalf("Put(replacement credential) error = %v", err)
	}
	if got, err := store.Get(context.Background(), ref); err != nil || got != "replacement-secret" {
		t.Fatalf("Get(replacement credential) = %q, %v", got, err)
	}
}

func TestStoreRejectsSymlinkCredential(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("anthropic", "anthropic@default")
	if err := ensureDir(store.root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte(`{"version":1,"ref":"`+ref+`","api_key":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.path(ref)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.Get(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("Get(symlink) error = %v", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("google", "google@default")
	if err := store.Put(context.Background(), ref, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), ref); !os.IsNotExist(err) {
		t.Fatalf("Get(deleted) error = %v", err)
	}
}
