package appserver

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSQLiteOperationStoreConcurrentChangedPayloadConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	first := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	second := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	stores := []OperationStore{first, second}
	start := make(chan struct{})
	type outcome struct {
		created bool
		err     error
	}
	outcomes := make(chan outcome, len(stores))
	for index, digest := range []string{"digest-a", "digest-b"} {
		go func(store OperationStore, digest string) {
			<-start
			_, created, err := store.Begin(
				context.Background(),
				operationStoreTestIntent("changed-request", digest),
			)
			outcomes <- outcome{created: created, err: err}
		}(stores[index], digest)
	}
	close(start)
	var created, conflicted int
	for range stores {
		result := <-outcomes
		switch {
		case result.err == nil && result.created:
			created++
		case errors.Is(result.err, ErrOperationConflict):
			conflicted++
		default:
			t.Fatalf("Begin() outcome = created %v, error %v", result.created, result.err)
		}
	}
	if created != 1 || conflicted != 1 {
		t.Fatalf("outcomes = %d created/%d conflicted, want 1/1", created, conflicted)
	}
}

func TestOperationStoreCompleteIsIdempotentAndNeverOverwrites(t *testing.T) {
	tests := []struct {
		name  string
		store func(*testing.T) OperationStore
	}{
		{name: "memory", store: func(*testing.T) OperationStore {
			return NewMemoryOperationStore()
		}},
		{name: "sqlite", store: func(t *testing.T) OperationStore {
			return newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.store(t)
			intent := operationStoreTestIntent("complete-once", "digest-a")
			if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
				t.Fatalf("Begin() = created %v, error %v", created, err)
			}
			want := CommandResult{
				OperationID: intent.OperationID,
				SessionID:   intent.SessionID,
				Outcome:     OutcomeCommitted,
				Revision:    7,
			}
			if _, err := store.Complete(context.Background(), intent, want); err != nil {
				t.Fatalf("Complete(first) error = %v", err)
			}
			if record, err := store.Complete(context.Background(), intent, want); err != nil || record.Result == nil || *record.Result != want {
				t.Fatalf("Complete(idempotent) = %#v, %v", record, err)
			}
			changed := want
			changed.Outcome = OutcomeRejected
			changed.Detail = "late writer"
			record, err := store.Complete(context.Background(), intent, changed)
			if !errors.Is(err, ErrOperationConflict) || record.Result == nil || *record.Result != want {
				t.Fatalf("Complete(changed) = %#v, %v; want original result and conflict", record, err)
			}
			reloaded, created, err := store.Begin(context.Background(), intent)
			if err != nil || created || reloaded.Result == nil || *reloaded.Result != want {
				t.Fatalf("reloaded record = %#v, created %v, error %v", reloaded, created, err)
			}
		})
	}
}

func TestOperationStoreResourceReceiptUsesValueSemantics(t *testing.T) {
	tests := []struct {
		name  string
		store func(*testing.T) OperationStore
	}{
		{name: "memory", store: func(*testing.T) OperationStore {
			return NewMemoryOperationStore()
		}},
		{name: "sqlite", store: func(t *testing.T) OperationStore {
			return newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.store(t)
			intent := operationStoreTestIntent("resource-receipt", "digest-resource")
			if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
				t.Fatalf("Begin() = created %v, error %v", created, err)
			}
			resource := CommandResource{Kind: CommandResourceACPPreparation, Ref: "opaque", Digest: "digest"}
			first := CommandResult{
				OperationID: intent.OperationID,
				SessionID:   intent.SessionID,
				Outcome:     OutcomeCommitted,
				Resource:    &resource,
			}
			if _, err := store.Complete(context.Background(), intent, first); err != nil {
				t.Fatalf("Complete(first) error = %v", err)
			}
			resource.Ref = "caller-mutated"
			want := first
			want.Resource = &CommandResource{Kind: CommandResourceACPPreparation, Ref: "opaque", Digest: "digest"}
			completed, err := store.Complete(context.Background(), intent, want)
			if err != nil || completed.Result == nil || !sameCommandResult(*completed.Result, want) {
				t.Fatalf("Complete(equal value) = %#v, %v", completed, err)
			}
			completed.Result.Resource.Ref = "returned-copy-mutated"
			reloaded, created, err := store.Begin(context.Background(), intent)
			if err != nil || created || reloaded.Result == nil || !sameCommandResult(*reloaded.Result, want) {
				t.Fatalf("Begin(reloaded) = %#v, created %v, error %v", reloaded, created, err)
			}
		})
	}
}

func TestSQLiteOperationStoreConcurrentCompleteChoosesOneImmutableResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	first := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	second := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	intent := operationStoreTestIntent("concurrent-complete", "digest-a")
	if _, created, err := first.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, error %v", created, err)
	}
	results := []CommandResult{
		{OperationID: intent.OperationID, SessionID: intent.SessionID, Outcome: OutcomeCommitted, Revision: 7},
		{OperationID: intent.OperationID, SessionID: intent.SessionID, Outcome: OutcomeRejected, Detail: "competing result"},
	}
	stores := []*SQLiteOperationStore{first, second}
	type completion struct {
		requested CommandResult
		record    OperationRecord
		err       error
	}
	start := make(chan struct{})
	completed := make(chan completion, len(results))
	for index, result := range results {
		go func(store *SQLiteOperationStore, result CommandResult) {
			<-start
			record, err := store.Complete(context.Background(), intent, result)
			completed <- completion{requested: result, record: record, err: err}
		}(stores[index], result)
	}
	close(start)
	var winner *CommandResult
	conflicts := 0
	for range results {
		result := <-completed
		switch {
		case result.err == nil:
			if winner != nil {
				t.Fatal("both competing Complete calls succeeded")
			}
			copyResult := result.requested
			winner = &copyResult
		case errors.Is(result.err, ErrOperationConflict):
			conflicts++
		default:
			t.Fatalf("Complete() error = %v", result.err)
		}
	}
	if winner == nil || conflicts != 1 {
		t.Fatalf("completion outcome = winner %#v, conflicts %d", winner, conflicts)
	}
	reloaded, created, err := first.Begin(context.Background(), intent)
	if err != nil || created || reloaded.Result == nil || *reloaded.Result != *winner {
		t.Fatalf("reloaded winner = %#v, created %v, error %v; want %#v", reloaded, created, err, *winner)
	}
}

func TestCommandServiceConcurrentDuplicateDoesNotStealCompletion(t *testing.T) {
	backend := &blockingOperationBackend{started: make(chan struct{}), release: make(chan struct{})}
	operations := &countingExecutionOperationStore{OperationStore: NewMemoryOperationStore()}
	service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
	principal := Principal{ID: "owner"}
	req := PromptRequest{
		WriteBase: WriteBase{OperationID: "in-flight", SessionID: "session-1"},
		Input:     "hello",
	}
	type response struct {
		result CommandResult
		err    error
	}
	first := make(chan response, 1)
	go func() {
		result, err := service.Prompt(context.Background(), principal, req)
		first <- response{result: result, err: err}
	}()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("first operation did not reach backend")
	}

	inFlight, err := service.Prompt(context.Background(), principal, req)
	if err != nil || inFlight.Outcome != OutcomeUnknown || backend.calls.Load() != 1 {
		t.Fatalf("in-flight retry = %#v, %v, calls %d", inFlight, err, backend.calls.Load())
	}
	if operations.executionCalls.Load() != 0 || backend.recoverCalls.Load() != 0 {
		t.Fatalf("non-recoverable command used execution/recovery = %d/%d", operations.executionCalls.Load(), backend.recoverCalls.Load())
	}
	close(backend.release)
	completed := <-first
	if completed.err != nil || completed.result.Outcome != OutcomeCommitted {
		t.Fatalf("creator completion = %#v, %v", completed.result, completed.err)
	}
	replayed, err := service.Prompt(context.Background(), principal, req)
	if err != nil || replayed != completed.result || backend.calls.Load() != 1 {
		t.Fatalf("completed retry = %#v, %v, calls %d", replayed, err, backend.calls.Load())
	}
}

func TestCommandServiceRecoveryWaitsForCanonicalWarningReceipt(t *testing.T) {
	tests := []struct {
		name   string
		stores func(*testing.T) (OperationStore, OperationStore)
	}{
		{
			name: "memory",
			stores: func(*testing.T) (OperationStore, OperationStore) {
				store := NewMemoryOperationStore()
				return store, store
			},
		},
		{
			name: "sqlite",
			stores: func(t *testing.T) (OperationStore, OperationStore) {
				path := filepath.Join(t.TempDir(), "control.sqlite")
				return newTestSQLiteOperationStore(t, path, OperationRetentionConfig{}),
					newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			firstStore, retryStore := test.stores(t)
			backend := &warningRecoveryBackend{
				started: make(chan struct{}),
				release: make(chan struct{}),
			}
			firstService := newTestCommandService(t, allowAuthorizer{}, firstStore, backend)
			retryService := newTestCommandService(t, allowAuthorizer{}, retryStore, backend)
			principal := Principal{ID: "owner"}
			req := PromptRequest{
				WriteBase: WriteBase{OperationID: "warning-receipt", SessionID: "session-1"},
				Input:     "hello",
			}
			type response struct {
				result CommandResult
				err    error
			}
			first := make(chan response, 1)
			go func() {
				result, err := firstService.Prompt(context.Background(), principal, req)
				first <- response{result: result, err: err}
			}()
			select {
			case <-backend.started:
			case <-time.After(time.Second):
				t.Fatal("creator did not reach backend")
			}

			retry := make(chan response, 1)
			go func() {
				result, err := retryService.Prompt(context.Background(), principal, req)
				retry <- response{result: result, err: err}
			}()
			select {
			case got := <-retry:
				t.Fatalf("retry returned before creator completed: %#v, %v", got.result, got.err)
			case <-time.After(50 * time.Millisecond):
			}

			close(backend.release)
			creatorResult := <-first
			retryResult := <-retry
			if creatorResult.err != nil || retryResult.err != nil {
				t.Fatalf("creator/retry errors = %v / %v", creatorResult.err, retryResult.err)
			}
			if !sameCommandResult(creatorResult.result, retryResult.result) || creatorResult.result.Detail != "durability warning" {
				t.Fatalf("creator/retry results = %#v / %#v", creatorResult.result, retryResult.result)
			}
			if got := backend.executeCalls.Load(); got != 1 {
				t.Fatalf("execute calls = %d, want 1", got)
			}
			if got := backend.recoverCalls.Load(); got != 0 {
				t.Fatalf("recovery calls = %d, want 0 while creator owns completion", got)
			}
		})
	}
}

func TestSQLiteOperationStoreExecutionGateIsProcessLocalAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	firstStore := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	secondStore := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	intent := operationStoreTestIntent("same-process-execution", "digest-a")
	first, err := firstStore.AcquireExecution(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = secondStore.AcquireExecution(ctx, intent)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireExecution() error = %v, want deadline", err)
	}
	first.Release()
	lease, err := secondStore.AcquireExecution(context.Background(), intent)
	if err != nil {
		t.Fatalf("AcquireExecution(after release) error = %v", err)
	}
	lease.Release()
}

func operationStoreTestIntent(operationID, digest string) OperationIntent {
	return OperationIntent{
		PrincipalID: "owner",
		OperationID: operationID,
		Action:      ActionPrompt,
		SessionID:   "session-1",
		Target:      "session-1",
		Digest:      digest,
	}
}

type blockingOperationBackend struct {
	started      chan struct{}
	release      chan struct{}
	startOnce    sync.Once
	calls        atomic.Int32
	recoverCalls atomic.Int32
}

type countingExecutionOperationStore struct {
	OperationStore
	executionCalls atomic.Int32
}

func (s *countingExecutionOperationStore) AcquireExecution(
	ctx context.Context,
	intent OperationIntent,
) (OperationExecutionLease, error) {
	s.executionCalls.Add(1)
	return s.OperationStore.AcquireExecution(ctx, intent)
}

type warningRecoveryBackend struct {
	started      chan struct{}
	release      chan struct{}
	startOnce    sync.Once
	executeCalls atomic.Int32
	recoverCalls atomic.Int32
}

func (b *warningRecoveryBackend) CanRecoverControlCommand(Action) bool {
	return true
}

func (b *warningRecoveryBackend) ExecuteControlCommand(
	ctx context.Context,
	_ Principal,
	_ Action,
	_ any,
) (CommandResult, error) {
	b.executeCalls.Add(1)
	b.startOnce.Do(func() { close(b.started) })
	select {
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	case <-b.release:
		return CommandResult{
			Outcome: OutcomeCommitted,
			Detail:  "durability warning",
			Resource: &CommandResource{
				Kind:   CommandResourceACPPreparation,
				Ref:    "opaque-ref",
				Digest: "preparation-digest",
			},
		}, nil
	}
}

func (b *warningRecoveryBackend) RecoverControlCommand(
	_ context.Context,
	_ Principal,
	_ OperationIntent,
	_ any,
) (CommandResult, bool, error) {
	b.recoverCalls.Add(1)
	return CommandResult{
		Outcome: OutcomeCommitted,
		Resource: &CommandResource{
			Kind:   CommandResourceACPPreparation,
			Ref:    "opaque-ref",
			Digest: "preparation-digest",
		},
	}, true, nil
}

func (b *blockingOperationBackend) ExecuteControlCommand(
	ctx context.Context,
	_ Principal,
	_ Action,
	_ any,
) (CommandResult, error) {
	b.calls.Add(1)
	b.startOnce.Do(func() { close(b.started) })
	select {
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	case <-b.release:
		return CommandResult{Outcome: OutcomeCommitted, Revision: 9}, nil
	}
}

func (b *blockingOperationBackend) CanRecoverControlCommand(Action) bool {
	return false
}

func (b *blockingOperationBackend) RecoverControlCommand(
	context.Context,
	Principal,
	OperationIntent,
	any,
) (CommandResult, bool, error) {
	b.recoverCalls.Add(1)
	return CommandResult{}, false, nil
}
