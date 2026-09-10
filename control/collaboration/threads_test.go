package collaboration

import (
	"context"
	"path/filepath"
	"testing"
)

type observationBackend struct {
	testBackend
	revision uint64
}

func (b *observationBackend) Read(_ context.Context, _, handle string, after uint64) (ThreadRead, error) {
	r := ThreadRead{Thread: Thread{ID: handle, Handle: handle, State: "completed"}, Cursor: b.revision}
	if b.revision > after {
		r.Output = "public result"
	}
	return r, nil
}

func TestThreadWaitUsesCursorAndMailboxRemainsDestructive(t *testing.T) {
	b := &observationBackend{revision: 2}
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := Identity{"work", "parent"}
	result, err := s.WaitThreads(t.Context(), i, []Target{{Handle: "b"}}, 0)
	if err != nil || result.Reason != "thread" || len(result.Threads) != 1 {
		t.Fatalf("first %v %v", result, err)
	}
	result, err = s.WaitThreads(t.Context(), i, []Target{{Handle: "a"}, {Handle: "b"}}, 0)
	if err != nil || len(result.Threads) != 2 {
		t.Fatalf("multiple targets: %v, %v", result, err)
	}
	result, err = s.WaitThreads(t.Context(), i, []Target{{Handle: "b", After: 2}}, 0)
	if err != nil || result.Reason != "timeout" {
		t.Fatalf("repeated %v %v", result, err)
	}
	_, err = s.Send(t.Context(), Identity{"work", "b"}, "parent", "reply", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.WaitThreads(t.Context(), i, nil, 0)
	if err != nil || result.Reason != "message" || len(result.Messages) != 1 {
		t.Fatalf("mail %v %v", result, err)
	}
	if mail, err := s.Receive(t.Context(), i); err != nil || len(mail) != 0 {
		t.Fatal("mail delivered twice")
	}
	if _, err = s.Read(t.Context(), i, Target{Handle: "parent"}); err == nil {
		t.Fatal("parent transcript exposed")
	}
	for _, def := range Definitions(false) {
		if def.Name == "CloseThread" || def.Name == "StartThread" {
			t.Fatalf("participant tool %s", def.Name)
		}
	}
}
