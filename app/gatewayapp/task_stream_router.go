package gatewayapp

import (
	"context"
	"iter"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

// hostTaskStreamService routes one Task observation to the Runtime that owns
// its Session. Task output remains an independent side stream; this router
// does not copy it into the Control Session feed.
type hostTaskStreamService struct {
	host *Stack
}

func (s hostTaskStreamService) Read(
	ctx context.Context,
	request stream.ReadRequest,
) (stream.Snapshot, error) {
	service, err := s.service(request.Ref.SessionID)
	if err != nil {
		return stream.Snapshot{}, err
	}
	return service.Read(ctx, request)
}

func (s hostTaskStreamService) Subscribe(
	ctx context.Context,
	request stream.SubscribeRequest,
) iter.Seq2[*stream.Frame, error] {
	return func(yield func(*stream.Frame, error) bool) {
		service, err := s.service(request.Ref.SessionID)
		if err != nil {
			yield(nil, err)
			return
		}
		for frame, streamErr := range service.Subscribe(ctx, request) {
			if !yield(frame, streamErr) {
				return
			}
		}
	}
}

func (s hostTaskStreamService) Wait(ctx context.Context, ref stream.Ref) (stream.Snapshot, error) {
	service, err := s.service(ref.SessionID)
	if err != nil {
		return stream.Snapshot{}, err
	}
	controller, ok := service.(stream.Controller)
	if !ok {
		return stream.Snapshot{}, taskStreamControlUnavailable()
	}
	return controller.Wait(ctx, ref)
}

func (s hostTaskStreamService) Kill(ctx context.Context, ref stream.Ref) error {
	service, err := s.service(ref.SessionID)
	if err != nil {
		return err
	}
	controller, ok := service.(stream.Controller)
	if !ok {
		return taskStreamControlUnavailable()
	}
	return controller.Kill(ctx, ref)
}

func (s hostTaskStreamService) Release(ctx context.Context, ref stream.Ref) error {
	service, err := s.service(ref.SessionID)
	if err != nil {
		return err
	}
	controller, ok := service.(stream.Controller)
	if !ok {
		return taskStreamControlUnavailable()
	}
	return controller.Release(ctx, ref)
}

func (s hostTaskStreamService) service(sessionID string) (stream.Service, error) {
	host := s.host
	if host == nil {
		return nil, taskStreamRuntimeUnavailable()
	}
	sessionID = strings.TrimSpace(sessionID)
	runtimeStack := host
	if registry := host.sessionRuntimes; registry != nil &&
		!registry.defaultSession(sessionID) {
		runtime, ok := registry.loaded(sessionID)
		if !ok || runtime == nil || runtime.stack == nil {
			return nil, taskStreamRuntimeUnavailable()
		}
		runtimeStack = runtime.stack
	}
	provider := runtimeStack.KernelStreams()
	if provider == nil || provider.Streams() == nil {
		return nil, taskStreamRuntimeUnavailable()
	}
	return provider.Streams(), nil
}

func taskStreamRuntimeUnavailable() error {
	return errorcode.New(errorcode.Unavailable, "gatewayapp: Task Runtime stream is unavailable")
}

func taskStreamControlUnavailable() error {
	return errorcode.New(errorcode.Unavailable, "gatewayapp: terminal stream control is unavailable")
}

var _ stream.Service = hostTaskStreamService{}
var _ stream.Controller = hostTaskStreamService{}
