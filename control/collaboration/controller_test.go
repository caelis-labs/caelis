package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type controllerTestBackend struct {
	mu      sync.Mutex
	threads []Thread
	started []string
}

func (b *controllerTestBackend) List(_ context.Context, id string) ([]Thread, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if id != "work" {
		return nil, errors.New("wrong Session")
	}
	return append([]Thread(nil), b.threads...), nil
}
func (*controllerTestBackend) Deliver(context.Context, string, []Message) error { return nil }
func (b *controllerTestBackend) Start(_ context.Context, i Identity, epoch string, _ json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.started = append(b.started, i.Session+":"+i.Member+":"+epoch)
	return json.RawMessage(`{"handle":"child","state":"running"}`), nil
}

func TestControllerGrantPromptLifecycleAndCreationAuthority(t *testing.T) {
	b := &controllerTestBackend{threads: []Thread{{Handle: "parent", ID: "epoch-1", SessionID: "remote-1"}, {Handle: "a", ID: "task-a", SessionID: "remote-a"}}}
	path := filepath.Join(t.TempDir(), "control.sqlite")
	s, err := Open(path, b)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	parent := Identity{"work", "parent"}
	g := s.Prepare(parent, "epoch-1")
	req := Request{Tool: "StartThread", Arguments: json.RawMessage(`{"agent":"breeze","prompt":"work"}`)}
	if _, err := s.CallAuthenticated(t.Context(), g.Token(), req); err == nil {
		t.Fatal("pending grant created work")
	}
	if err := g.Bind("remote-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CallAuthenticated(t.Context(), g.Token(), req); err != nil {
		t.Fatal(err)
	}
	if len(b.started) != 1 || b.started[0] != "work:parent:epoch-1" {
		t.Fatal(b.started)
	}
	if _, err := s.Call(t.Context(), parent, req); err == nil {
		t.Fatal("unbound caller created work")
	}
	child := s.Prepare(Identity{"work", "a"}, "task-a")
	if err := child.Bind("remote-a"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"StartThread", "ReadThread", "WaitThread"} {
		if _, err := s.CallAuthenticated(t.Context(), child.Token(), Request{Tool: name, Arguments: json.RawMessage(`{"role":"controller"}`)}); err == nil {
			t.Fatalf("child obtained %s", name)
		}
	}
	text, err := g.PreparePrompt(t.Context())
	if err != nil || !strings.Contains(text, "maintainer") || !strings.Contains(text, "StartThread") {
		t.Fatalf("setup: %q %v", text, err)
	}
	for _, private := range []string{g.Token(), "epoch-1", "remote-1"} {
		if strings.Contains(text, private) {
			t.Fatal("setup leaked execution identity")
		}
	}
	if text, err := g.PreparePrompt(t.Context()); err != nil || text != "" {
		t.Fatalf("repeat: %q %v", text, err)
	}
	// A renewed connection preserves the remote context, not its credential.
	renewed := s.Prepare(parent, "epoch-1")
	if err := renewed.Bind("remote-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(t.Context(), g.Token()); err == nil {
		t.Fatal("renewal retained old credential")
	}
	if text, err := renewed.PreparePrompt(t.Context()); err != nil || text != "" {
		t.Fatalf("renewal injected setup: %q %v", text, err)
	}
	oldToken := renewed.Token()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, b)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(t.Context(), oldToken); err == nil {
		t.Fatal("restart retained process credential")
	}
	g = s.Prepare(parent, "epoch-1")
	if err := g.Bind("remote-1"); err != nil {
		t.Fatal(err)
	}
	if text, err := g.PreparePrompt(t.Context()); err != nil || text != "" {
		t.Fatalf("restart injected setup: %q %v", text, err)
	}
	b.mu.Lock()
	b.threads[0].ID = "epoch-2"
	b.threads[0].SessionID = "remote-2"
	b.mu.Unlock()
	if _, _, err := s.Authenticate(t.Context(), g.Token()); err == nil {
		t.Fatal("handoff retained old credential")
	}
	g = s.Prepare(parent, "epoch-2")
	if err := g.Bind("remote-2"); err != nil {
		t.Fatal(err)
	}
	if text, err := g.PreparePrompt(t.Context()); err != nil || text == "" {
		t.Fatalf("new remote missed setup: %q %v", text, err)
	}
	if err := g.ForgetPrompt(t.Context()); err != nil {
		t.Fatal(err)
	}
	if text, err := g.PreparePrompt(t.Context()); err != nil || text == "" {
		t.Fatalf("proven non-submission missed setup: %q %v", text, err)
	}
}

func TestSharedReaderReplacementDoesNotInheritCursorOrCredential(t *testing.T) {
	b := &controllerTestBackend{threads: []Thread{{Handle: "parent", ID: "epoch", SessionID: "remote"}, {Handle: "a", ID: "old", SessionID: "old-remote"}, {Handle: "b", ID: "b", SessionID: "b"}}}
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Send(t.Context(), Identity{"work", "parent"}, "b", "shared before joining", ""); err != nil {
		t.Fatal(err)
	}
	g := s.Prepare(Identity{"work", "a"}, "old")
	if err := g.Bind("old-remote"); err != nil {
		t.Fatal(err)
	}
	req := Request{Tool: "ReadMessages", Arguments: json.RawMessage(`{}`)}
	if _, err := s.CallAuthenticated(t.Context(), g.Token(), req); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.threads[1].ID = "new"
	b.threads[1].SessionID = "new-remote"
	b.mu.Unlock()
	if _, err := s.CallAuthenticated(t.Context(), g.Token(), req); err == nil {
		t.Fatal("replaced member retained access")
	}
	page, err := s.ReadMessages(t.Context(), Identity{"work", "a"}, nil, 32)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("new instance inherited cursor: %#v %v", page, err)
	}
}
