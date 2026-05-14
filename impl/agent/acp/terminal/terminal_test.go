package terminal

import (
	"context"
	"iter"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/stream"
)

func TestLocalTerminalAdapterOutputUsesCumulativeRead(t *testing.T) {
	t.Parallel()

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

type recordingTerminalService struct {
	readReq  stream.ReadRequest
	snapshot stream.Snapshot
}

func (s *recordingTerminalService) Read(_ context.Context, req stream.ReadRequest) (stream.Snapshot, error) {
	s.readReq = req
	return s.snapshot, nil
}

func (s *recordingTerminalService) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(func(*stream.Frame, error) bool) {}
}
