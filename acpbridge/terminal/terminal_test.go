package terminal

import (
	"context"
	"iter"
	"testing"

	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
)

func TestLocalTerminalAdapterOutputUsesCumulativeRead(t *testing.T) {
	t.Parallel()

	service := &recordingTerminalService{
		snapshot: sdkstream.Snapshot{
			Frames: []sdkstream.Frame{{Stream: "stdout", Text: "one\ntwo\n"}},
			Cursor: sdkstream.Cursor{Stdout: 8},
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
	if service.readReq.Cursor.Stdout != 0 || service.readReq.Cursor.Stderr != 0 {
		t.Fatalf("Read cursor = %+v, want zero cursor for ACP cumulative output", service.readReq.Cursor)
	}
}

type recordingTerminalService struct {
	readReq  sdkstream.ReadRequest
	snapshot sdkstream.Snapshot
}

func (s *recordingTerminalService) Read(_ context.Context, req sdkstream.ReadRequest) (sdkstream.Snapshot, error) {
	s.readReq = req
	return s.snapshot, nil
}

func (s *recordingTerminalService) Subscribe(context.Context, sdkstream.SubscribeRequest) iter.Seq2[*sdkstream.Frame, error] {
	return func(func(*sdkstream.Frame, error) bool) {}
}
