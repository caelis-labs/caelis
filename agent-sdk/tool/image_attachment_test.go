package tool

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestReferenceResultImagesPreservesOriginalOnPartialFailure(t *testing.T) {
	unavailable := errors.New("publication failed")
	var calls int
	writer := testAttachmentWriter(func(_ context.Context, owner string, data []byte) (model.AttachmentRef, error) {
		calls++
		if calls == 2 {
			return model.AttachmentRef{}, unavailable
		}
		return model.AttachmentRef{SessionID: owner, Digest: "test", Instance: "image", Size: int64(len(data))}, nil
	})
	ctx := model.WithImageAttachments(t.Context(), "owner", nil, writer, nil)
	first, second := []byte("first bytes"), []byte("second bytes")
	result := Result{Content: []ContentBlock{Image{Data: first}, Text("text"), Image{Data: second}}}
	got, err := ReferenceResultImages(ctx, result)
	if !errors.Is(err, unavailable) || calls != 2 {
		t.Fatalf("publications=%d, error=%v", calls, err)
	}
	for _, content := range [][]ContentBlock{result.Content, got.Content} {
		if len(content) != 3 || content[1] != Text("text") {
			t.Fatalf("changed original blocks: %#v", content)
		}
		for i, want := range map[int][]byte{0: first, 2: second} {
			img, ok := content[i].(Image)
			if !ok || !bytes.Equal(img.Data, want) || img.Attachment != (model.AttachmentRef{}) {
				t.Fatalf("changed original image: %#v", content[i])
			}
		}
	}
}

type testAttachmentWriter func(context.Context, string, []byte) (model.AttachmentRef, error)

func (w testAttachmentWriter) StoreAttachment(ctx context.Context, owner string, data []byte) (model.AttachmentRef, error) {
	return w(ctx, owner, data)
}
