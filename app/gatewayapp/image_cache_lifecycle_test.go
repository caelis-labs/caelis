package gatewayapp

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type closingAttachmentProbe struct {
	started, cancelled, release chan struct{}
}

func (p closingAttachmentProbe) OpenAttachment(ctx context.Context, _ string, _ model.AttachmentRef) (io.ReadCloser, error) {
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	<-p.release
	return nil, ctx.Err()
}

func TestStackCloseJoinsImagePrewarm(t *testing.T) {
	probe := closingAttachmentProbe{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	defer close(probe.release)
	cache := model.NewImagePreparationCache()
	stack := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{imageCache: cache}}}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	wait := func(done <-chan struct{}) {
		t.Helper()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("Host image cache did not reach the expected shutdown boundary")
		}
	}
	const id = "session"
	ref := model.AttachmentRef{SessionID: id, Instance: "one", Size: 64, Width: 2, Height: 2}
	msg := model.NewMessage(model.RoleUser, model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceAttachment, Attachment: ref}, "image/png", "image"))
	model.PrewarmImages(model.WithImageAttachments(ctx, id, probe, nil, cache), &msg)
	wait(probe.started)

	closed := make(chan struct{})
	var closeErr error
	go func() {
		closeErr = stack.Close()
		close(closed)
	}()
	wait(probe.cancelled)
	select {
	case <-closed:
		t.Fatal("Host returned before its image worker released the attachment")
	default:
	}
	probe.release <- struct{}{}
	wait(closed)
	if closeErr != nil || ctx.Err() != nil {
		t.Fatalf("Host shutdown result=%v Run context=%v", closeErr, ctx.Err())
	}
	// The caller's Run context is still alive, but the Host cannot start a
	// second worker. A second probe open would close an already closed channel.
	model.PrewarmImages(model.WithImageAttachments(ctx, id, probe, nil, cache), &msg)
}
