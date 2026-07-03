// Package memory contains an in-memory stream service implementation.
package memory

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/ports/stream"
)

type Service struct {
	mu      sync.RWMutex
	streams map[string][]stream.Frame
	closed  map[string]bool
}

func New() *Service {
	return &Service{
		streams: map[string][]stream.Frame{},
		closed:  map[string]bool{},
	}
}

func (s *Service) Read(ctx context.Context, req stream.ReadRequest) (stream.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return stream.Snapshot{}, err
	}
	if s == nil {
		return stream.Snapshot{}, fmt.Errorf("stream/memory: service is nil")
	}
	ref := stream.NormalizeRef(req.Ref)
	key := streamKey(ref)
	s.mu.RLock()
	frames := append([]stream.Frame(nil), s.streams[key]...)
	closed := s.closed[key]
	s.mu.RUnlock()
	start := int(req.Cursor.Events)
	if start < 0 {
		start = 0
	}
	if start > len(frames) {
		start = len(frames)
	}
	out := make([]stream.Frame, 0, len(frames)-start)
	for _, frame := range frames[start:] {
		out = append(out, stream.CloneFrame(frame))
	}
	return stream.Snapshot{
		Ref:     ref,
		Cursor:  stream.Cursor{Events: int64(len(frames))},
		Frames:  out,
		Running: !closed,
	}, nil
}

func (s *Service) Subscribe(ctx context.Context, req stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(yield func(*stream.Frame, error) bool) {
		snapshot, err := s.Read(ctx, stream.ReadRequest{
			Ref:    req.Ref,
			Cursor: req.Cursor,
		})
		if err != nil {
			yield(nil, err)
			return
		}
		for _, frame := range snapshot.Frames {
			frame := frame
			if !yield(&frame, nil) {
				return
			}
		}
	}
}

func (s *Service) PublishStream(frame stream.Frame) {
	if s == nil {
		return
	}
	frame = stream.CloneFrame(frame)
	frame.Ref = stream.NormalizeRef(frame.Ref)
	if frame.UpdatedAt.IsZero() {
		frame.UpdatedAt = time.Now()
	}
	key := streamKey(frame.Ref)
	s.mu.Lock()
	if s.streams == nil {
		s.streams = map[string][]stream.Frame{}
	}
	if s.closed == nil {
		s.closed = map[string]bool{}
	}
	s.streams[key] = append(s.streams[key], frame)
	if frame.Closed {
		s.closed[key] = true
	}
	s.mu.Unlock()
}

func (s *Service) Wait(ctx context.Context, ref stream.Ref) (stream.Snapshot, error) {
	snapshot, err := s.Read(ctx, stream.ReadRequest{Ref: ref})
	if err != nil {
		return stream.Snapshot{}, err
	}
	snapshot.Running = false
	return snapshot, nil
}

func (s *Service) Kill(ctx context.Context, ref stream.Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("stream/memory: service is nil")
	}
	key := streamKey(stream.NormalizeRef(ref))
	s.mu.Lock()
	if s.closed == nil {
		s.closed = map[string]bool{}
	}
	s.closed[key] = true
	s.mu.Unlock()
	return nil
}

func (s *Service) Release(ctx context.Context, ref stream.Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("stream/memory: service is nil")
	}
	key := streamKey(stream.NormalizeRef(ref))
	s.mu.Lock()
	delete(s.streams, key)
	delete(s.closed, key)
	s.mu.Unlock()
	return nil
}

func streamKey(ref stream.Ref) string {
	return strings.Join([]string{
		strings.TrimSpace(ref.SessionID),
		strings.TrimSpace(ref.TaskID),
		strings.TrimSpace(ref.TerminalID),
	}, "\x00")
}

var _ stream.Service = (*Service)(nil)
var _ stream.Sink = (*Service)(nil)
var _ stream.Controller = (*Service)(nil)
