package sessionprofile

import (
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
