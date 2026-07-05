package memory

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestPublishStreamAndRead(t *testing.T) {
	t.Parallel()

	svc := New()
	ref := stream.Ref{SessionID: "sess-1", TaskID: "task-1", TerminalID: "term-1"}
	ctx := context.Background()

	svc.PublishStream(stream.Frame{Ref: ref, Text: "hello"})
	svc.PublishStream(stream.Frame{Ref: ref, Text: "world"})

	snap, err := svc.Read(ctx, stream.ReadRequest{Ref: ref})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got, want := len(snap.Frames), 2; got != want {
		t.Fatalf("len(snap.Frames) = %d, want %d", got, want)
	}
	if got := snap.Frames[0].Text; got != "hello" {
		t.Fatalf("first frame text = %q, want %q", got, "hello")
	}
	if got := snap.Frames[1].Text; got != "world" {
		t.Fatalf("second frame text = %q, want %q", got, "world")
	}
	if !snap.Running {
		t.Fatal("snap.Running = false, want true before close")
	}
	if got := snap.Cursor.Events; got != 2 {
		t.Fatalf("snap.Cursor.Events = %d, want 2", got)
	}

	incremental, err := svc.Read(ctx, stream.ReadRequest{
		Ref:    ref,
		Cursor: stream.Cursor{Events: 1},
	})
	if err != nil {
		t.Fatalf("Read(cursor=1) error = %v", err)
	}
	if got, want := len(incremental.Frames), 1; got != want {
		t.Fatalf("len(incremental.Frames) = %d, want %d", got, want)
	}
	if got := incremental.Frames[0].Text; got != "world" {
		t.Fatalf("incremental frame text = %q, want %q", got, "world")
	}
}

func TestClosedFrameSetsRunningFalse(t *testing.T) {
	t.Parallel()

	svc := New()
	ref := stream.Ref{SessionID: "sess-1", TaskID: "task-1", TerminalID: "term-1"}
	ctx := context.Background()

	svc.PublishStream(stream.Frame{Ref: ref, Text: "output"})
	svc.PublishStream(stream.Frame{Ref: ref, Text: "done", Closed: true})

	snap, err := svc.Read(ctx, stream.ReadRequest{Ref: ref})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if snap.Running {
		t.Fatal("snap.Running = true, want false after closed frame")
	}
	if got, want := len(snap.Frames), 2; got != want {
		t.Fatalf("len(snap.Frames) = %d, want %d", got, want)
	}
}

func TestReleaseClearsStream(t *testing.T) {
	t.Parallel()

	svc := New()
	ref := stream.Ref{SessionID: "sess-1", TaskID: "task-1", TerminalID: "term-1"}
	ctx := context.Background()

	svc.PublishStream(stream.Frame{Ref: ref, Text: "before release", Closed: true})
	if err := svc.Release(ctx, ref); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	snap, err := svc.Read(ctx, stream.ReadRequest{Ref: ref})
	if err != nil {
		t.Fatalf("Read() after Release error = %v", err)
	}
	if got, want := len(snap.Frames), 0; got != want {
		t.Fatalf("len(snap.Frames) after Release = %d, want %d", got, want)
	}
	if !snap.Running {
		t.Fatal("snap.Running = false, want true for released stream")
	}

	svc.PublishStream(stream.Frame{Ref: ref, Text: "after release"})
	snap, err = svc.Read(ctx, stream.ReadRequest{Ref: ref})
	if err != nil {
		t.Fatalf("Read() after republish error = %v", err)
	}
	if got, want := len(snap.Frames), 1; got != want {
		t.Fatalf("len(snap.Frames) after republish = %d, want %d", got, want)
	}
	if got := snap.Frames[0].Text; got != "after release" {
		t.Fatalf("republished frame text = %q, want %q", got, "after release")
	}
}
