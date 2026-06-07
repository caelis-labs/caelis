package terminal

import (
	"context"
	"iter"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/stream"
)

func TestLocalTerminalAdapterOutputUsesCumulativeRead(t *testing.T) {
	service := &recordingTerminalService{
		snapshot: stream.Snapshot{
			Frames: []stream.Frame{{Text: "one\ntwo\n"}},
			Cursor: stream.Cursor{Output: 8},
		},
	}
	adapter := LocalTerminalAdapter{Streams: service}

	resp, err := adapter.Output(context.Background(), TerminalOutputRequest{
		SessionID:  "session-1",
		TerminalID: "terminal-1",
	})
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if resp.Output != "one\ntwo\n" {
		t.Fatalf("Output() = %q, want cumulative terminal output", resp.Output)
	}
	if service.readReq.Cursor.Output != 0 {
		t.Fatalf("Read cursor = %+v, want zero cursor for ACP cumulative output", service.readReq.Cursor)
	}
}

func TestLocalTerminalAdapterOutputSuppressesNoOutputPlaceholder(t *testing.T) {
	adapter := LocalTerminalAdapter{Streams: &recordingTerminalService{
		snapshot: stream.Snapshot{
			FinalText: "(no output)",
			ExitCode:  intPtr(0),
		},
	}}

	resp, err := adapter.Output(context.Background(), TerminalOutputRequest{
		SessionID:  "session-1",
		TerminalID: "terminal-1",
	})
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if resp.Output != "" {
		t.Fatalf("Output() = %q, want empty captured terminal output", resp.Output)
	}
	if resp.ExitStatus == nil || resp.ExitStatus.ExitCode == nil || *resp.ExitStatus.ExitCode != 0 {
		t.Fatalf("ExitStatus = %#v, want exit code 0", resp.ExitStatus)
	}
}

func TestLocalTerminalAdapterControlMethodsUseResolvedRef(t *testing.T) {
	service := &recordingTerminalService{
		snapshot: stream.Snapshot{ExitCode: intPtr(7)},
	}
	adapter := LocalTerminalAdapter{
		Streams: service,
		ResolveRef: func(sessionID string, terminalID string) (stream.Ref, bool) {
			if sessionID != "session-1" || terminalID != "display-terminal" {
				t.Fatalf("resolve input = %q/%q", sessionID, terminalID)
			}
			return stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "runtime-terminal"}, true
		},
	}

	wait, err := adapter.WaitForExit(context.Background(), TerminalWaitForExitRequest{
		SessionID:  "session-1",
		TerminalID: "display-terminal",
	})
	if err != nil {
		t.Fatalf("WaitForExit() error = %v", err)
	}
	if wait.ExitCode == nil || *wait.ExitCode != 7 {
		t.Fatalf("WaitForExit() = %#v, want exit code 7", wait)
	}
	if err := adapter.Kill(context.Background(), TerminalKillRequest{SessionID: "session-1", TerminalID: "display-terminal"}); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	if err := adapter.Release(context.Background(), TerminalReleaseRequest{SessionID: "session-1", TerminalID: "display-terminal"}); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if service.waitRef.TerminalID != "runtime-terminal" || service.killRef.TerminalID != "runtime-terminal" || service.releaseRef.TerminalID != "runtime-terminal" {
		t.Fatalf("refs = wait %#v kill %#v release %#v", service.waitRef, service.killRef, service.releaseRef)
	}
}

func intPtr(value int) *int {
	return &value
}

type recordingTerminalService struct {
	readReq    stream.ReadRequest
	readRef    stream.Ref
	waitRef    stream.Ref
	killRef    stream.Ref
	releaseRef stream.Ref
	snapshot   stream.Snapshot
}

func (s *recordingTerminalService) Read(_ context.Context, req stream.ReadRequest) (stream.Snapshot, error) {
	s.readReq = req
	s.readRef = req.Ref
	return s.snapshot, nil
}

func (s *recordingTerminalService) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(func(*stream.Frame, error) bool) {}
}

func (s *recordingTerminalService) Wait(_ context.Context, ref stream.Ref) (stream.Snapshot, error) {
	s.waitRef = ref
	return s.snapshot, nil
}

func (s *recordingTerminalService) Kill(_ context.Context, ref stream.Ref) error {
	s.killRef = ref
	return nil
}

func (s *recordingTerminalService) Release(_ context.Context, ref stream.Ref) error {
	s.releaseRef = ref
	return nil
}
