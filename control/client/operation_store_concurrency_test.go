package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileOperationStoreConcurrentBeginAcrossInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	intent := operationStoreTestIntent("same-request", "digest-a")
	const workers = 32
	start := make(chan struct{})
	errs := make(chan error, workers)
	var created atomic.Int32
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, wasCreated, err := NewFileOperationStore(root).Begin(context.Background(), intent)
			if err != nil {
				errs <- err
				return
			}
			if wasCreated {
				created.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Begin() error = %v", err)
	}
	if got := created.Load(); got != 1 {
		t.Fatalf("created count = %d, want exactly one", got)
	}
}

func TestFileOperationStoreConcurrentChangedPayloadConflicts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	start := make(chan struct{})
	type outcome struct {
		created bool
		err     error
	}
	outcomes := make(chan outcome, 2)
	for _, digest := range []string{"digest-a", "digest-b"} {
		digest := digest
		go func() {
			<-start
			_, created, err := NewFileOperationStore(root).Begin(
				context.Background(),
				operationStoreTestIntent("changed-request", digest),
			)
			outcomes <- outcome{created: created, err: err}
		}()
	}
	close(start)
	var created, conflicted int
	for range 2 {
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
		store OperationStore
	}{
		{name: "memory", store: NewMemoryOperationStore()},
		{name: "file", store: NewFileOperationStore(filepath.Join(t.TempDir(), "operations"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := operationStoreTestIntent("complete-once", "digest-a")
			if _, created, err := test.store.Begin(context.Background(), intent); err != nil || !created {
				t.Fatalf("Begin() = created %v, error %v", created, err)
			}
			want := CommandResult{
				OperationID: intent.OperationID,
				SessionID:   intent.SessionID,
				Outcome:     OutcomeCommitted,
				Revision:    7,
			}
			if _, err := test.store.Complete(context.Background(), intent, want); err != nil {
				t.Fatalf("Complete(first) error = %v", err)
			}
			if record, err := test.store.Complete(context.Background(), intent, want); err != nil || record.Result == nil || *record.Result != want {
				t.Fatalf("Complete(idempotent) = %#v, %v", record, err)
			}
			changed := want
			changed.Outcome = OutcomeRejected
			changed.Detail = "late writer"
			record, err := test.store.Complete(context.Background(), intent, changed)
			if !errors.Is(err, ErrOperationConflict) || record.Result == nil || *record.Result != want {
				t.Fatalf("Complete(changed) = %#v, %v; want original result and conflict", record, err)
			}
			reloaded, created, err := test.store.Begin(context.Background(), intent)
			if err != nil || created || reloaded.Result == nil || *reloaded.Result != want {
				t.Fatalf("reloaded record = %#v, created %v, error %v", reloaded, created, err)
			}
		})
	}
}

func TestFileOperationStoreConcurrentCompleteChoosesOneImmutableResult(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	intent := operationStoreTestIntent("concurrent-complete", "digest-a")
	if _, created, err := NewFileOperationStore(root).Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, error %v", created, err)
	}
	results := []CommandResult{
		{OperationID: intent.OperationID, SessionID: intent.SessionID, Outcome: OutcomeCommitted, Revision: 7},
		{OperationID: intent.OperationID, SessionID: intent.SessionID, Outcome: OutcomeRejected, Detail: "competing result"},
	}
	type completion struct {
		requested CommandResult
		record    OperationRecord
		err       error
	}
	start := make(chan struct{})
	completed := make(chan completion, len(results))
	for _, result := range results {
		result := result
		go func() {
			<-start
			record, err := NewFileOperationStore(root).Complete(context.Background(), intent, result)
			completed <- completion{requested: result, record: record, err: err}
		}()
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
	reloaded, created, err := NewFileOperationStore(root).Begin(context.Background(), intent)
	if err != nil || created || reloaded.Result == nil || *reloaded.Result != *winner {
		t.Fatalf("reloaded winner = %#v, created %v, error %v; want %#v", reloaded, created, err, *winner)
	}
}

func TestCommandServiceConcurrentDuplicateDoesNotStealCompletion(t *testing.T) {
	backend := &blockingOperationBackend{started: make(chan struct{}), release: make(chan struct{})}
	service := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), backend)
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

func TestFileOperationStoreBeginExactlyOnceAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-process lock test")
	}
	root := filepath.Join(t.TempDir(), "operations")
	barrier := filepath.Join(t.TempDir(), "start")
	const processes = 2
	commands := make([]*exec.Cmd, processes)
	outputs := make([]bytes.Buffer, processes)
	results := make([]string, processes)
	ready := make([]string, processes)
	for index := range processes {
		results[index] = filepath.Join(t.TempDir(), "result.json")
		ready[index] = filepath.Join(t.TempDir(), "ready")
		command := exec.Command(os.Args[0], "-test.run=^TestFileOperationStoreProcessHelper$")
		command.Env = append(os.Environ(),
			"CAELIS_OPERATION_STORE_HELPER=begin",
			"CAELIS_OPERATION_STORE_ROOT="+root,
			"CAELIS_OPERATION_STORE_BARRIER="+barrier,
			"CAELIS_OPERATION_STORE_READY="+ready[index],
			"CAELIS_OPERATION_STORE_RESULT="+results[index],
		)
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		if err := command.Start(); err != nil {
			t.Fatalf("start helper %d: %v", index, err)
		}
		commands[index] = command
	}
	waitForOperationStoreFiles(t, ready, 5*time.Second)
	if err := os.WriteFile(barrier, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s", index, err, outputs[index].String())
		}
	}

	created := 0
	for _, path := range results {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var result operationStoreProcessResult
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Error != "" {
			t.Fatalf("helper Begin() error = %s", result.Error)
		}
		if result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("cross-process created count = %d, want exactly one", created)
	}
}

func TestFileOperationStoreCompleteAndSweepAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-process lock test")
	}
	root := filepath.Join(t.TempDir(), "operations")
	intent := operationStoreTestIntent("cross-process-complete-sweep", "digest-a")
	if _, created, err := NewFileOperationStore(root).Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, error %v", created, err)
	}
	barrier := filepath.Join(t.TempDir(), "start")
	modes := []string{"complete", "sweep"}
	commands := make([]*exec.Cmd, len(modes))
	outputs := make([]bytes.Buffer, len(modes))
	results := make([]string, len(modes))
	ready := make([]string, len(modes))
	for index, mode := range modes {
		results[index] = filepath.Join(t.TempDir(), "result.json")
		ready[index] = filepath.Join(t.TempDir(), "ready")
		command := exec.Command(os.Args[0], "-test.run=^TestFileOperationStoreProcessHelper$")
		command.Env = append(os.Environ(),
			"CAELIS_OPERATION_STORE_HELPER="+mode,
			"CAELIS_OPERATION_STORE_ROOT="+root,
			"CAELIS_OPERATION_STORE_BARRIER="+barrier,
			"CAELIS_OPERATION_STORE_READY="+ready[index],
			"CAELIS_OPERATION_STORE_RESULT="+results[index],
		)
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		if err := command.Start(); err != nil {
			t.Fatalf("start %s helper: %v", mode, err)
		}
		commands[index] = command
	}
	waitForOperationStoreFiles(t, ready, 5*time.Second)
	if err := os.WriteFile(barrier, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("%s helper: %v\n%s", modes[index], err, outputs[index].String())
		}
		data, err := os.ReadFile(results[index])
		if err != nil {
			t.Fatal(err)
		}
		var result operationStoreProcessResult
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Error != "" {
			t.Fatalf("%s helper error = %s", modes[index], result.Error)
		}
	}

	want := operationStoreTestResult(intent, OutcomeCommitted)
	reloaded, created, err := NewFileOperationStore(root).Begin(context.Background(), intent)
	if err != nil || created || reloaded.Result == nil || *reloaded.Result != want {
		t.Fatalf("reloaded = %#v, created %v, error %v; want %#v", reloaded, created, err, want)
	}
}

func TestOperationStoreOSFileLockHonorsContextCancellation(t *testing.T) {
	root := t.TempDir()
	first, err := lockOperationStoreRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlockOperationStoreRoot(first); err != nil {
			t.Errorf("unlock first operation store: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	second, err := lockOperationStoreRoot(ctx, root)
	if second != nil {
		_ = unlockOperationStoreRoot(second)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("cancelled lock wait took %v", elapsed)
	}
}

func TestFileOperationStoreProcessHelper(t *testing.T) {
	mode := os.Getenv("CAELIS_OPERATION_STORE_HELPER")
	if mode == "" {
		return
	}
	ready := os.Getenv("CAELIS_OPERATION_STORE_READY")
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	barrier := os.Getenv("CAELIS_OPERATION_STORE_BARRIER")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for helper barrier")
		}
		time.Sleep(5 * time.Millisecond)
	}
	store := NewFileOperationStore(os.Getenv("CAELIS_OPERATION_STORE_ROOT"))
	var (
		created bool
		err     error
	)
	switch mode {
	case "begin":
		_, created, err = store.Begin(
			context.Background(),
			operationStoreTestIntent("cross-process", "digest-a"),
		)
	case "complete":
		intent := operationStoreTestIntent("cross-process-complete-sweep", "digest-a")
		_, err = store.Complete(context.Background(), intent, operationStoreTestResult(intent, OutcomeCommitted))
	case "sweep":
		_, err = store.Sweep(context.Background())
	default:
		t.Fatalf("unknown operation store helper mode %q", mode)
	}
	result := operationStoreProcessResult{Created: created}
	if err != nil {
		result.Error = err.Error()
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if writeErr := os.WriteFile(os.Getenv("CAELIS_OPERATION_STORE_RESULT"), data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
}

type operationStoreProcessResult struct {
	Created bool   `json:"created"`
	Error   string `json:"error,omitempty"`
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

func waitForOperationStoreFiles(t *testing.T, paths []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		allReady := true
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				allReady = false
			}
		}
		if allReady {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for operation store helpers")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type blockingOperationBackend struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	calls     atomic.Int32
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
