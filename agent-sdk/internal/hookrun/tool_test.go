package hookrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestInvokeProducerFailureRemainsOriginal(t *testing.T) {
	for _, around := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-middleware", true: "passthrough"}[around], func(t *testing.T) {
			original := errors.New("producer persistence failed")
			hooks := hook.Set{}
			if around {
				hooks.ToolInvoke = []hook.ToolInvoke{{Name: "pass", Apply: func(ctx context.Context, _ hook.ToolContext, next hook.ToolNext) (tool.Result, error) {
					return next(ctx)
				}}}
			}
			ctx := WithRun(t.Context(), hooks, hook.TurnContext{})
			calls := 0
			result, err := InvokeTool(ctx, tool.Definition{Name: "Effect"}, tool.Call{ID: "one", Name: "Effect"}, func(context.Context) (tool.Result, error) {
				calls++
				return tool.Result{Content: []tool.ContentBlock{tool.Text("original failure")}}, original
			})
			var processing *ProcessingError
			if !errors.Is(err, original) || errors.As(err, &processing) || !strings.Contains(err.Error(), original.Error()) || len(result.Content) != 1 || result.Content[0] != tool.Text("original failure") || calls != 1 {
				t.Fatalf("producer failure hidden: calls=%d result=%+v error=%v", calls, result, err)
			}
		})
	}
}

func TestFullResultRejectsDuplicateBusinessBlocksWithoutLosingEffect(t *testing.T) {
	r := tool.Result{Content: []tool.ContentBlock{
		tool.StructuredContent{Value: json.RawMessage(`1`)},
		tool.StructuredContent{Value: json.RawMessage(`2`)},
	}}
	r, err := FullResult(t.Context(), tool.Definition{}, tool.Call{}, r, nil)
	var processing *ProcessingError
	if !errors.As(err, &processing) || processing.EffectError != nil || len(r.Content) != 1 || r.Content[0].ContentType() != tool.ContentTypeText {
		t.Fatalf("invalid result did not become a processing failure with a known effect: %+v, %v", r, err)
	}
}

func TestInvokeInvalidPostprocessingRetainsKnownEffect(t *testing.T) {
	hooks := hook.Set{ToolInvoke: []hook.ToolInvoke{{Name: "invalid-content", Apply: func(ctx context.Context, _ hook.ToolContext, next hook.ToolNext) (tool.Result, error) {
		r, e := next(ctx)
		r.Content = []tool.ContentBlock{tool.StructuredContent{Value: []byte("invalid")}}
		return r, e
	}}}}
	calls := 0
	r, err := InvokeTool(WithRun(t.Context(), hooks, hook.TurnContext{}), tool.Definition{Name: "Effect"}, tool.Call{ID: "one", Name: "Effect"}, func(context.Context) (tool.Result, error) {
		calls++
		return tool.Result{Content: []tool.ContentBlock{tool.Text("known committed business value")}}, nil
	})
	var phase *hook.Error
	var processing *ProcessingError
	if !errors.As(err, &phase) || phase.Phase != "tool_invoke" || phase.Name != "invalid-content" || !errors.As(err, &processing) || EffectError(err) != nil || calls != 1 || len(r.Content) != 1 || strings.Contains(string(r.Content[0].(tool.Text)), "known committed business value") {
		t.Fatalf("known effect lost: calls=%d result=%+v err=%v", calls, r, err)
	}
}

func TestInvokeCannotSuppressProducerFailure(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.New("authority revoked"), errors.New("store write failed"), tool.FailedReply("tool reported failure")} {
		t.Run(cause.Error(), func(t *testing.T) {
			hooks := hook.Set{ToolInvoke: []hook.ToolInvoke{{Name: "suppress", Apply: func(ctx context.Context, _ hook.ToolContext, next hook.ToolNext) (tool.Result, error) {
				r, _ := next(ctx)
				return r, nil
			}}}}
			calls := 0
			_, err := InvokeTool(WithRun(t.Context(), hooks, hook.TurnContext{}), tool.Definition{Name: "Effect"}, tool.Call{ID: "one", Name: "Effect"}, func(context.Context) (tool.Result, error) { calls++; return tool.Result{}, cause })
			var phase *hook.Error
			effectErr := EffectError(err)
			if !errors.Is(err, cause) || !errors.As(err, &phase) || phase.Name != "suppress" || !errors.Is(effectErr, cause) || calls != 1 {
				t.Fatalf("producer failure suppressed: calls=%d err=%v effectErr=%v", calls, err, effectErr)
			}
		})
	}
}

func TestModelViewMediaSources(t *testing.T) {
	ref := model.AttachmentRef{SessionID: "owner", Digest: "digest", Instance: "original", Size: 70}
	reader := testAttachmentReader(func(_ context.Context, owner string, got model.AttachmentRef) (io.ReadCloser, error) {
		if owner != ref.SessionID || got != ref {
			return nil, model.ErrAttachmentUnavailable
		}
		return io.NopCloser(strings.NewReader("")), nil
	})
	attachment := model.MediaSource{Kind: model.MediaSourceAttachment, Attachment: ref}
	mixed := attachment
	mixed.Data = "aW1n"
	missing := attachment
	missing.Attachment.Instance = "stale"
	for _, tc := range []struct {
		name, owner string
		source      model.MediaSource
		modality    model.MediaModality
		ok          bool
	}{
		{"owned", "owner", attachment, model.MediaModalityImage, true},
		{"foreign", "other", attachment, model.MediaModalityImage, false},
		{"unbound", "", attachment, model.MediaModalityImage, false},
		{"stale", "owner", missing, model.MediaModalityImage, false},
		{"mixed", "owner", mixed, model.MediaModalityImage, false},
		{"audio", "owner", attachment, model.MediaModalityAudio, false},
		{"inline", "", model.MediaSource{Kind: model.MediaSourceInline, Data: "aW1n"}, model.MediaModalityImage, true},
		{"empty inline", "owner", model.MediaSource{Kind: model.MediaSourceInline}, model.MediaModalityImage, false},
		{"empty URL", "owner", model.MediaSource{Kind: model.MediaSourceURL}, model.MediaModalityImage, false},
		{"unknown kind", "owner", model.MediaSource{Kind: "unknown", Data: "aW1n"}, model.MediaModalityImage, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := model.WithImageAttachments(t.Context(), tc.owner, reader, nil, nil)
			part := model.NewMediaPart(tc.modality, tc.source, "image/png", "")
			if err := validateModelViewParts(ctx, []model.Part{part}); (err == nil) != tc.ok {
				t.Fatalf("validateModelViewParts = %v, want success %t", err, tc.ok)
			}
		})
	}
}

type testAttachmentReader func(context.Context, string, model.AttachmentRef) (io.ReadCloser, error)

func (r testAttachmentReader) OpenAttachment(ctx context.Context, owner string, ref model.AttachmentRef) (io.ReadCloser, error) {
	return r(ctx, owner, ref)
}
