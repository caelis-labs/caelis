package collaboration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type userInputTestBackend struct {
	threads []Thread
	inputs  []UserInput
	err     error
	listErr error
}

func (b *userInputTestBackend) List(context.Context, string) ([]Thread, error) {
	return b.threads, b.listErr
}
func (*userInputTestBackend) Deliver(context.Context, string, []Message) error {
	return errors.New("human input used Agent mailbox")
}
func (b *userInputTestBackend) DeliverUserInput(_ context.Context, input UserInput) error {
	b.inputs = append(b.inputs, input)
	return b.err
}
func userInputTestTarget() Thread {
	return Thread{ID: "task", ParticipantID: "participant", SessionID: "child", Generation: "generation", CanDeliver: true}
}
func TestUserInputWaitsAndKeepsExactTargetAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	backend := &userInputTestBackend{threads: []Thread{userInputTestTarget()}}
	backend.threads[0].CanDeliver = false
	service, err := Open(path, backend)
	if err != nil {
		t.Fatal(err)
	}
	parts := []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: "aGk="}}
	status, err := service.EnqueueUserInput(t.Context(), "input", "user", "parent", "participant", "task", "guide", parts)
	if err != nil || status.State != "queued" {
		t.Fatalf("enqueue = %#v, %v", status, err)
	}
	pending, err := service.pendingUserInputs(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%v,%v", pending, err)
	}
	if err := service.deliverUserInput(t.Context(), backend, pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(backend.inputs) != 0 {
		t.Fatal("dispatched into non-steering active child")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	service, err = Open(path, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	backend.threads[0].CanDeliver = true
	pending, _ = service.pendingUserInputs(t.Context())
	if err := service.deliverUserInput(t.Context(), backend, pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(backend.inputs) != 1 || backend.inputs[0].UserID != "user" || backend.inputs[0].Generation != "generation" {
		t.Fatalf("dispatch=%#v", backend.inputs)
	}
	if len(backend.inputs[0].ContentParts) != 1 || backend.inputs[0].ContentParts[0].Data != "aGk=" {
		t.Fatal("restart lost queued image")
	}
	// Replaying the same operation after detachment returns its original receipt.
	backend.threads = nil
	status, err = service.EnqueueUserInput(t.Context(), "input", "user", "parent", "participant", "task", "guide", parts)
	if err != nil || status.State != "sent" {
		t.Fatalf("replay=%#v,%v", status, err)
	}
	parts[0].Data = "Ynk="
	if _, err := service.EnqueueUserInput(t.Context(), "input", "user", "parent", "participant", "task", "guide", parts); !errorcode.Is(err, errorcode.Conflict) {
		t.Fatalf("different image reused operation: %v", err)
	}
	if _, err = service.EnqueueUserInput(t.Context(), "input", "user", "parent", "participant", "task", "changed", nil); !errorcode.Is(err, errorcode.Conflict) {
		t.Fatalf("reused operation=%v", err)
	}
	statuses, err := service.UserInputStatuses(t.Context(), "other-user", "parent", []string{"input"})
	if err != nil || statuses[0].State != "unknown" {
		t.Fatalf("cross-user receipt=%#v,%v", statuses, err)
	}
}
func TestUserInputUnknownDispatchNeverRetriesAndReplacementNeverReceives(t *testing.T) {
	for _, mode := range []string{"unknown", "restart", "replacement", "closed"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.sqlite")
			backend := &userInputTestBackend{threads: []Thread{userInputTestTarget()}}
			service, err := Open(path, backend)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.EnqueueUserInput(t.Context(), "input", "user", "parent", "participant", "task", "guide", nil)
			if err != nil {
				t.Fatal(err)
			}
			inputs, _ := service.pendingUserInputs(t.Context())
			want := "unknown"
			switch mode {
			case "unknown":
				backend.err = errors.New("connection lost after dispatch")
			case "replacement":
				backend.threads[0].Generation = "replacement"
				want = "failed"
			case "closed":
				backend.listErr = ErrSessionClosed
				want = "failed"
			case "restart":
				_, err = service.db.Exec(`UPDATE collaboration_user_inputs SET state='sending'`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode != "restart" {
				if err = service.deliverUserInput(t.Context(), backend, inputs[0]); err != nil {
					t.Fatal(err)
				}
			}
			_ = service.Close()
			service, err = Open(path, backend)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = service.Close() }()
			statuses, err := service.UserInputStatuses(t.Context(), "user", "parent", []string{"input"})
			if err != nil || statuses[0].State != want {
				t.Fatalf("receipt=%#v,%v", statuses, err)
			}
			inputs, err = service.pendingUserInputs(t.Context())
			if err != nil || len(inputs) != 0 {
				t.Fatalf("unsafe retry=%#v,%v", inputs, err)
			}
			if mode != "unknown" && len(backend.inputs) != 0 {
				t.Fatalf("wrong dispatch=%#v", backend.inputs)
			}
		})
	}
}
func TestUserInputPendingSelectionDoesNotStarveOtherChildren(t *testing.T) {
	backend := &userInputTestBackend{threads: []Thread{userInputTestTarget(), {ID: "other", ParticipantID: "other", SessionID: "other", Generation: "g", CanDeliver: true}}}
	service, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	for i := range 80 {
		if _, err := service.EnqueueUserInput(t.Context(), fmt.Sprint(i), "user", "parent", "participant", "task", fmt.Sprint(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.EnqueueUserInput(t.Context(), "other", "user", "parent", "other", "other", "other", nil); err != nil {
		t.Fatal(err)
	}
	inputs, err := service.pendingUserInputs(t.Context())
	if err != nil || len(inputs) != 2 || inputs[0].Text != "0" || inputs[1].Text != "other" {
		t.Fatalf("pending=%#v,%v", inputs, err)
	}
}

func TestUserInputRetriesOnlyProvenNonAdmission(t *testing.T) {
	backend := &userInputTestBackend{threads: []Thread{userInputTestTarget()}, err: errorcode.Wrap(errorcode.FailedPrecondition, "finishing", agent.ErrChildInputNotReady)}
	backend.threads[0].Generation = "" // Spawn identity is already pinned by Task and child Session.
	service, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	if _, err = service.EnqueueUserInput(t.Context(), "input", "user", "parent", "participant", "task", "guide", nil); err != nil {
		t.Fatal(err)
	}
	pending, err := service.pendingUserInputs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = service.deliverUserInput(t.Context(), backend, pending[0]); err != nil {
		t.Fatal(err)
	}
	statuses, _ := service.UserInputStatuses(t.Context(), "user", "parent", []string{"input"})
	if statuses[0].State != "queued" {
		t.Fatalf("non-admission=%#v", statuses)
	}
	backend.err = nil
	if err = service.deliverUserInput(t.Context(), backend, pending[0]); err != nil {
		t.Fatal(err)
	}
	statuses, _ = service.UserInputStatuses(t.Context(), "user", "parent", []string{"input"})
	if statuses[0].State != "sent" || len(backend.inputs) != 2 || backend.inputs[0].ID != backend.inputs[1].ID {
		t.Fatalf("admission=%#v inputs=%#v", statuses, backend.inputs)
	}
}

type concurrentUserInputBackend struct {
	entered chan UserInput
	release chan struct{}
}

func (b *concurrentUserInputBackend) List(context.Context, string) ([]Thread, error) {
	return []Thread{{ID: "a", ParticipantID: "a", SessionID: "child-a", CanDeliver: true}, {ID: "b", ParticipantID: "b", SessionID: "child-b", CanDeliver: true}}, nil
}
func (*concurrentUserInputBackend) Deliver(context.Context, string, []Message) error {
	return errors.New("Agent mail used")
}
func (b *concurrentUserInputBackend) DeliverUserInput(ctx context.Context, in UserInput) error {
	select {
	case b.entered <- in:
	case <-ctx.Done():
		return ctx.Err()
	}
	if in.ID == "a1" {
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func TestUserInputWorkersSerializeRecipientsAndDrain(t *testing.T) {
	backend := &concurrentUserInputBackend{entered: make(chan UserInput, 3), release: make(chan struct{})}
	service, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	for _, id := range []string{"a1", "a2", "b1"} {
		if _, err = service.EnqueueUserInput(t.Context(), id, "user", "parent", id[:1], id[:1], id, nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); service.runUserInputs(ctx, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("input workers did not drain")
		}
	})
	seen := map[string]bool{}
	for range 2 {
		select {
		case in := <-backend.entered:
			seen[in.ID] = true
		case <-time.After(3 * time.Second):
			t.Fatal("independent recipient stalled")
		}
	}
	if !seen["a1"] || !seen["b1"] {
		t.Fatalf("delivery order=%v", seen)
	}
	close(backend.release)
	select {
	case in := <-backend.entered:
		if in.ID != "a2" {
			t.Fatalf("next=%#v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("next input did not dispatch")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown failed")
	}
}

func TestUserInputParticipantFacingCopy(t *testing.T) {
	backend := &userInputTestBackend{threads: []Thread{userInputTestTarget()}}
	service, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()

	if _, err := service.EnqueueUserInput(t.Context(), "incomplete", "user", "parent", "participant", "", "guide", nil); err == nil ||
		err.Error() != "User input requires an exact participant, text or images, and at most 65536 text bytes" {
		t.Fatalf("incomplete target error = %v", err)
	}
	backend.threads = nil
	if _, err := service.EnqueueUserInput(t.Context(), "detached", "user", "parent", "participant", "task", "guide", nil); err == nil ||
		err.Error() != "The selected participant is no longer attached" {
		t.Fatalf("detached target error = %v", err)
	}
	backend.threads = []Thread{{ID: "task", ParticipantID: "participant", SessionID: "child", Generation: "generation", State: "unknown_outcome", CanDeliver: true}}
	if _, err := service.EnqueueUserInput(t.Context(), "unresolved", "user", "parent", "participant", "task", "guide", nil); err == nil ||
		err.Error() != "Participant execution is unresolved" {
		t.Fatalf("unresolved target error = %v", err)
	}

	backend.threads = []Thread{userInputTestTarget()}
	if _, err := service.EnqueueUserInput(t.Context(), "detached-delivery", "user", "parent", "participant", "task", "guide", nil); err != nil {
		t.Fatal(err)
	}
	pending, err := service.pendingUserInputs(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v,%v", pending, err)
	}
	backend.threads = nil
	if err := service.deliverUserInput(t.Context(), backend, pending[0]); err != nil {
		t.Fatal(err)
	}
	if statuses, err := service.UserInputStatuses(t.Context(), "user", "parent", []string{"detached-delivery"}); err != nil || len(statuses) != 1 ||
		statuses[0].State != "failed" || statuses[0].Detail != "Participant detached before delivery" {
		t.Fatalf("detached delivery receipt = %#v, %v", statuses, err)
	}

	backend.threads = []Thread{userInputTestTarget()}
	if _, err := service.EnqueueUserInput(t.Context(), "unresolved-delivery", "user", "parent", "participant", "task", "guide", nil); err != nil {
		t.Fatal(err)
	}
	pending, err = service.pendingUserInputs(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v,%v", pending, err)
	}
	backend.threads[0].State = "unknown_outcome"
	if err := service.deliverUserInput(t.Context(), backend, pending[0]); err != nil {
		t.Fatal(err)
	}
	if statuses, err := service.UserInputStatuses(t.Context(), "user", "parent", []string{"unresolved-delivery"}); err != nil || len(statuses) != 1 ||
		statuses[0].State != "unknown" || statuses[0].Detail != "Participant execution is unresolved; input was not sent" {
		t.Fatalf("unresolved delivery receipt = %#v, %v", statuses, err)
	}
}
