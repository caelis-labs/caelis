package application

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

func TestResourceExpiryImmutableRetryAndRestart(t *testing.T) {
	s, path := testStore(t)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	owner := testConnection(t, s, 1)
	binding := testBinding(t, s, owner, "session")
	foreign := testConnection(t, s, 2)
	foreignBinding := testBinding(t, s, foreign, "foreign-session")
	data := []byte("owned immutable media")
	deadline := now.Add(2 * time.Minute).In(time.FixedZone("request", 3600))
	r, err := s.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "photo.png", "image/png", data, &deadline)
	if err != nil {
		t.Fatal(err)
	}
	wantExpiry := deadline.UTC()
	if r.ExpiresAt == nil || !r.ExpiresAt.Equal(wantExpiry) || r.ExpiresAt.Location() != time.UTC {
		t.Fatalf("expiry = %v, want %v UTC", r.ExpiresAt, wantExpiry)
	}
	deadline = deadline.Add(time.Hour) // caller-owned timestamp cannot mutate the stored descriptor
	data[0] = 'X'
	readDescriptor, readData, err := s.ReadResource(t.Context(), owner.Scope, binding.SessionID, r.ID)
	if err != nil || !reflect.DeepEqual(readDescriptor, r) || !bytes.Equal(readData, []byte("owned immutable media")) {
		t.Fatalf("resource read: %+v %q %v", readDescriptor, readData, err)
	}
	for _, id := range []struct {
		scope   Scope
		session string
	}{{foreign.Scope, binding.SessionID}, {foreign.Scope, foreignBinding.SessionID}} {
		_, _, err = s.ReadResource(t.Context(), id.scope, id.session, r.ID)
		assertError(t, err, ErrNotFound)
		_, err = s.GetResource(t.Context(), id.scope, id.session, r.ID)
		assertError(t, err, ErrNotFound)
	}
	// Equivalent instants are the same immutable upload, regardless of timezone.
	retryExpiry := wantExpiry.In(time.FixedZone("other", -3600))
	retry, err := s.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "photo.png", "image/png", []byte("owned immutable media"), &retryExpiry)
	if err != nil || !reflect.DeepEqual(retry, r) {
		t.Fatalf("exact retry: %+v %v", retry, err)
	}
	for _, changed := range []*time.Time{nil, &deadline} {
		_, err = s.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "photo.png", "image/png", []byte("owned immutable media"), changed)
		assertError(t, err, ErrConflict)
	}
	_, err = s.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "photo.png", "image/png", []byte("different"), &retryExpiry)
	assertError(t, err, ErrConflict)

	// Expiry is an immutable byte-use deadline, not a descriptor/receipt deletion.
	// A valid original retry also survives both resource and connection lease expiry.
	now = wantExpiry.Add(LeaseDuration)
	got, err := s.GetResource(t.Context(), owner.Scope, binding.SessionID, r.ID)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("expired descriptor: %+v %v", got, err)
	}
	_, readData, err = s.ReadResource(t.Context(), owner.Scope, binding.SessionID, r.ID)
	assertError(t, err, ErrResourceExpired)
	if len(readData) != 0 {
		t.Fatalf("expired byte read exposed data: %q", readData)
	}
	// The internal callback byte path shares the same expiry gate.
	_, readData, err = s.resource(t.Context(), owner.Scope, binding.SessionID, r.ID)
	assertError(t, err, ErrResourceExpired)
	if len(readData) != 0 {
		t.Fatalf("expired callback byte read exposed data: %q", readData)
	}
	retry, err = s.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "photo.png", "image/png", []byte("owned immutable media"), &retryExpiry)
	if err != nil || !reflect.DeepEqual(retry, r) {
		t.Fatalf("expired retry: %+v %v", retry, err)
	}
	_, err = s.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "photo.png", "image/png", []byte("owned immutable media"), &deadline)
	assertError(t, err, ErrConflict)
	_, err = s.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "new-upload", "photo.png", "image/png", []byte("owned immutable media"), &deadline)
	assertError(t, err, ErrLeaseExpired)

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	reopened.now = func() time.Time { return now }
	retry, err = reopened.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "photo.png", "image/png", []byte("owned immutable media"), &retryExpiry)
	if err != nil || !reflect.DeepEqual(retry, r) {
		t.Fatalf("restarted retry: %+v %v", retry, err)
	}
	_, readData, err = reopened.ReadResource(t.Context(), owner.Scope, binding.SessionID, r.ID)
	assertError(t, err, ErrResourceExpired)
	if len(readData) != 0 {
		t.Fatalf("restarted byte read exposed data: %q", readData)
	}
	if err := reopened.Revoke(t.Context(), owner.Scope); err != nil {
		t.Fatal(err)
	}
	retry, err = reopened.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "photo.png", "image/png", []byte("owned immutable media"), &retryExpiry)
	if err != nil || !reflect.DeepEqual(retry, r) {
		t.Fatalf("revoked exact receipt: %+v %v", retry, err)
	}
}

func TestResourceExpiryValidationAndPermanentBaseline(t *testing.T) {
	s, _ := testStore(t)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	owner := testConnection(t, s, 1)
	binding := testBinding(t, s, owner, "session")
	for _, deadline := range []time.Time{now, now.Add(-time.Nanosecond)} {
		_, err := s.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "x", "text/plain", []byte("a"), &deadline)
		assertError(t, err, ErrInvalid)
	}
	future := now.Add(time.Second)
	r, err := s.CreateResourceWithExpiry(t.Context(), owner.Scope, binding.SessionID, "upload", "x", "text/plain", []byte("a"), &future)
	if err != nil {
		t.Fatal(err)
	}
	permanent, err := s.CreateResource(t.Context(), owner.Scope, binding.SessionID, "baseline", "log.txt", "text/plain", []byte("permanent"))
	if err != nil || permanent.ExpiresAt != nil {
		t.Fatalf("baseline create: %+v %v", permanent, err)
	}
	now = future // Exact expiry boundary prohibits byte use; permanent artifacts remain readable.
	_, data, err := s.ReadResource(t.Context(), owner.Scope, binding.SessionID, r.ID)
	assertError(t, err, ErrResourceExpired)
	if len(data) != 0 {
		t.Fatalf("expired byte read exposed data: %q", data)
	}
	if err := s.Revoke(t.Context(), owner.Scope); err != nil {
		t.Fatal(err)
	}
	got, data, err := s.ReadResource(t.Context(), owner.Scope, binding.SessionID, permanent.ID)
	if err != nil || !reflect.DeepEqual(got, permanent) || !bytes.Equal(data, []byte("permanent")) {
		t.Fatalf("permanent read after revoke: %+v %q %v", got, data, err)
	}
}
