package runtime

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func appendTestEventWithGuard(t *testing.T, h *asyncHarness, event *session.Event) {
	t.Helper()
	if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: event}); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionPreparedImageSource(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewGray(image.Rect(0, 0, 2100, 1100))); err != nil {
		t.Fatal(err)
	}
	message := model.NewMessage(model.RoleUser, model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: base64.StdEncoding.EncodeToString(data.Bytes())}, "image/png", "screenshot.png"))
	event := &session.Event{Type: session.EventTypeUser, Message: &message}
	for _, mode := range []string{"async", "idle"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "idle" {
				h, actual := completedIdleSource(t, event)
				job := prepareIdleJob(t, h)
				if !reflect.DeepEqual(job.candidate.frozen.Messages[:len(actual.Messages)], actual.Messages) {
					t.Fatal("idle lost prepared prefix")
				}
				return
			}
			h := newAsyncHarness(t, "")
			h.llm.imageInput = true
			appendTestEventWithGuard(t, h, event)
			run := h.run(t)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- asyncToolResponse("first")
			next := receiveSnapshotValue(t, h.llm.foreground)
			hash, err := requestsource.Fingerprint(next.request.Messages)
			if err != nil || next.request.Source == nil || next.request.Source.MessagesHash != hash || next.scope == nil || next.scope.job == nil {
				t.Fatal("image normalization invalidated bound dispatch source")
			}
			background := receiveSnapshotValue(t, h.llm.background)
			want := model.CloneRequest(next.request)
			want.Messages = append(want.Messages, model.NewTextMessage(model.RoleUser, inContextCompactionPrompt))
			if !reflect.DeepEqual(background, want) {
				t.Fatal("summary changed prepared foreground prefix")
			}
			h.completeCandidate(t, next.scope)
			h.llm.responses <- asyncTextResponse("done", 500)
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
