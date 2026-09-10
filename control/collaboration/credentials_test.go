package collaboration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type credentialBackend struct{ threads []Thread }

func (b *credentialBackend) List(context.Context, string) ([]Thread, error) { return b.threads, nil }
func (*credentialBackend) Deliver(context.Context, string, []Message) error { return nil }

func TestGrantExpiryRevocationAndActivationBinding(t *testing.T) {
	b := &credentialBackend{threads: []Thread{{ID: "instance-a", Handle: "a", SessionID: "remote-a"}}}
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	s.now = func() time.Time { return now }
	id := Identity{"work", "a"}
	g := s.Prepare(id, "instance-a")
	if _, _, err = s.Authenticate(t.Context(), g.Token()); err == nil {
		t.Fatal("pending grant authorized")
	}
	if err = g.Bind("remote-a"); err != nil {
		t.Fatal(err)
	}
	expires := g.expires
	now = now.Add(time.Hour)
	if err = g.Bind("remote-a"); err != nil || g.expires != expires {
		t.Fatal("bind renewed credential")
	}
	if err = g.Bind("remote-b"); err == nil {
		t.Fatal("cross-ACP binding")
	}
	if got, _, err := s.Authenticate(t.Context(), g.Token()); err != nil || got != id {
		t.Fatalf("scope %v %v", got, err)
	}
	now = expires
	if _, _, err = s.Authenticate(t.Context(), g.Token()); err == nil {
		t.Fatal("expired grant authorized")
	}
	g = s.Prepare(id, "instance-a")
	if err = g.Bind("remote-a"); err != nil {
		t.Fatal(err)
	}
	replacement := s.Prepare(id, "instance-a")
	if err = replacement.Bind("remote-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Authenticate(t.Context(), g.Token()); err == nil {
		t.Fatal("previous activation remained authorized")
	}
	b.threads[0].ID = "instance-b"
	if _, _, err = s.Authenticate(t.Context(), replacement.Token()); err == nil {
		t.Fatal("reused handle inherited authority")
	}
	b.threads[0].ID = "instance-a"
	if _, _, err = s.Authenticate(t.Context(), replacement.Token()); err == nil {
		t.Fatal("revoked grant resurrected")
	}
	g = s.Prepare(id, "instance-a")
	_ = g.Bind("remote-a")
	g.Close()
	if _, _, err = s.Authenticate(t.Context(), g.Token()); err == nil {
		t.Fatal("closed grant authorized")
	}
}

func TestGrantCannotSelectAnotherWorkSession(t *testing.T) {
	s := openTestService(t, &testBackend{})
	g := s.Prepare(Identity{"work", "b"}, "b")
	if err := g.Bind("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CallAuthenticated(t.Context(), g.Token(), Request{Tool: "ListThreads", Arguments: []byte(`{"session":"other"}`)}); err == nil {
		t.Fatal("accepted caller-selected Session")
	}
	s.Revoke(Identity{"work", "b"})
	if _, err := s.CallAuthenticated(t.Context(), g.Token(), Request{Tool: "ListThreads", Arguments: []byte(`{}`)}); err == nil {
		t.Fatal("revocation ignored")
	}
}

// Discovery failures must not change an otherwise live grant's lifetime.
type failingCredentialBackend struct {
	credentialBackend
	err error
}

func (b *failingCredentialBackend) List(ctx context.Context, id string) ([]Thread, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.err != nil {
		return nil, b.err
	}
	return b.credentialBackend.List(ctx, id)
}

func TestGrantSurvivesDiscoveryFailure(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("temporary store failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			b := &failingCredentialBackend{credentialBackend: credentialBackend{threads: []Thread{{ID: "instance", Handle: "a", SessionID: "remote"}}}}
			s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			g := s.Prepare(Identity{"work", "a"}, "instance")
			if err := g.Bind("remote"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Authenticate(t.Context(), g.Token()); err != nil {
				t.Fatal(err)
			}
			expires := g.expires
			ctx := t.Context()
			if errors.Is(failure, context.Canceled) {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else {
				b.err = failure
			}
			if _, _, err := s.Authenticate(ctx, g.Token()); !errors.Is(err, failure) {
				t.Fatalf("authentication error = %v, want %v", err, failure)
			}
			b.err = nil
			if _, deadline, err := s.Authenticate(t.Context(), g.Token()); err != nil || deadline != expires {
				t.Fatalf("live grant changed after discovery failure: deadline=%v, error=%v", deadline, err)
			}
		})
	}
}
