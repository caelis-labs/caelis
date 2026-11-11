package sessionprofile

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConfigurationCheckpointGuardOrdersUpdateWithoutAdmission(t *testing.T) {
	s, _ := testStore(t)
	owner := testOwner(t, s, 1)
	binding := testBinding(t, s, owner, "session")
	release, err := s.ValidateConfiguration(t.Context(), owner.Scope, binding.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if unlock, err := s.ValidateConfiguration(t.Context(), owner.Scope, binding.SessionID, 1); unlock != nil || !errors.Is(err, ErrConfigurationStale) {
		t.Fatal("contended guard admitted")
	}
	changed := make(chan error, 1)
	go func() {
		_, err := s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "during-checkpoint", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Instructions: pointer("new instructions")}}, nil)
		changed <- err
	}()
	select {
	case err := <-changed:
		release()
		t.Fatalf("update crossed commit guard: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	release()
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	current, err := s.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || current.Revision != 2 || current.LastRequest != nil {
		t.Fatalf("validation fabricated admission: %+v %v", current, err)
	}
	if unlock, err := s.ValidateConfiguration(t.Context(), owner.Scope, binding.SessionID, 1); unlock != nil || !errors.Is(err, ErrConfigurationStale) {
		t.Fatal("old profile accepted")
	}
}

func TestConfigurationHostGuardWaitsAndRejectsChangedRevision(t *testing.T) {
	s, _ := testStore(t)
	owner := testOwner(t, s, 1)
	binding := testBinding(t, s, owner, "session")
	release, err := s.ValidateConfiguration(t.Context(), owner.Scope, binding.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancelled := make(chan error, 1)
	go func() { _, err := s.GuardConfiguration(ctx, owner.Scope, binding.SessionID, 1); cancelled <- err }()
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting cancellation=%v", err)
	}
	waited := make(chan error, 1)
	go func() {
		guard, err := s.GuardConfiguration(t.Context(), owner.Scope, binding.SessionID, 1)
		if guard != nil {
			guard()
		}
		waited <- err
	}()
	select {
	case err := <-waited:
		t.Fatalf("contended guard returned early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if err := <-waited; err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "replace-before-host", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Instructions: pointer("new")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if guard, err := s.GuardConfiguration(t.Context(), owner.Scope, binding.SessionID, 1); guard != nil || !errors.Is(err, ErrConfigurationStale) {
		t.Fatalf("old configuration admitted: %v", err)
	}
}
