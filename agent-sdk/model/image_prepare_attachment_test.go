package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"io"
	"testing"
	"time"
)

type attachmentReadProbe struct {
	images map[AttachmentRef][]byte
	reads  map[AttachmentRef]int
	opens  int
}

type attachmentReadCounter struct {
	*bytes.Reader
	ref   AttachmentRef
	probe *attachmentReadProbe
}

func (p attachmentReadCounter) Read(b []byte) (int, error) {
	p.probe.reads[p.ref]++
	return p.Reader.Read(b)
}

func (p attachmentReadProbe) add(instance string, data []byte) AttachmentRef {
	ref := AttachmentRef{SessionID: "session", Instance: instance, Digest: fmt.Sprintf("%x", sha256.Sum256(data)), Size: int64(len(data))}
	p.images[ref] = data
	return ref
}

func (p *attachmentReadProbe) OpenAttachment(_ context.Context, id string, ref AttachmentRef) (io.ReadCloser, error) {
	p.opens++
	data, ok := p.images[ref]
	if !ok || id != ref.SessionID {
		return nil, ErrAttachmentUnavailable
	}
	return io.NopCloser(attachmentReadCounter{Reader: bytes.NewReader(data), ref: ref, probe: p}), nil
}

func TestAttachmentPreparationChecksHotAvailabilityAndOversizeDoesNotEvict(t *testing.T) {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 5, 5))); err != nil {
		t.Fatal(err)
	}
	probe := &attachmentReadProbe{images: map[AttachmentRef][]byte{}, reads: map[AttachmentRef]int{}}
	small := probe.add("first", pngBytes.Bytes())
	large := probe.add("large", append(bytes.Clone(pngBytes.Bytes()), make([]byte, 1024)...))
	second := probe.add("second", pngBytes.Bytes())
	cache := newImagePreparationCache(550, 1, time.Minute)
	t.Cleanup(func() { clearImagePreparationCache(cache) })
	ctx := WithImageAttachments(t.Context(), "session", probe, nil, cache)
	prepare := func(ref AttachmentRef) preparedImage {
		t.Helper()
		value, err := cache.prepareAttachment(ctx, &MediaPart{Source: MediaSource{Kind: MediaSourceAttachment, Attachment: ref}}, false)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	prepare(small)
	reads := probe.reads[small]
	if reads == 0 {
		t.Fatal("cold lookup did not read the attachment body")
	}
	if len(prepare(large).images) != 1 {
		t.Fatal("oversized cache entry was not delivered")
	}
	prepare(small)
	if probe.reads[small] != reads {
		t.Fatal("oversized entry evicted the hot image")
	}
	prepare(second)
	prepare(small)
	if probe.reads[small] <= reads {
		t.Fatal("entry limit did not evict the old image")
	}
	delete(probe.images, small)
	if len(prepare(small).images) != 0 || probe.opens != 6 {
		t.Fatalf("hot entry bypassed current availability (opens=%d)", probe.opens)
	}
}

type attachmentReaderFunc func(context.Context, string, AttachmentRef) (io.ReadCloser, error)

func (f attachmentReaderFunc) OpenAttachment(ctx context.Context, id string, ref AttachmentRef) (io.ReadCloser, error) {
	return f(ctx, id, ref)
}

func TestAttachmentPrewarmDropsOverlapAndLeavesForegroundIndependent(t *testing.T) {
	pixel := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, pixel); err != nil {
		t.Fatal(err)
	}
	probe := attachmentReadProbe{images: map[AttachmentRef][]byte{}}
	warming := probe.add("warming", pngBytes.Bytes())
	dropped := probe.add("dropped", pngBytes.Bytes())
	foreground := probe.add("foreground", pngBytes.Bytes())
	entered := make(chan struct{})
	release := make(chan struct{})
	reader := attachmentReaderFunc(func(ctx context.Context, _ string, ref AttachmentRef) (io.ReadCloser, error) {
		switch ref {
		case warming:
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case dropped:
			t.Error("warm queue admitted overlapping job")
		}
		return io.NopCloser(bytes.NewReader(probe.images[ref])), nil
	})
	cache := newImagePreparationCache(1<<20, 4, time.Minute)
	t.Cleanup(func() { clearImagePreparationCache(cache) })
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ctx = WithImageAttachments(ctx, "session", reader, nil, cache)
	src := func(ref AttachmentRef) MediaSource { return MediaSource{Kind: MediaSourceAttachment, Attachment: ref} }
	message := func(ref AttachmentRef) *Message {
		msg := NewMessage(RoleUser, NewMediaPart(MediaModalityImage, src(ref), "image/png", "picture"))
		return &msg
	}
	PrewarmImages(ctx, message(warming))
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("warm job never opened the selected attachment")
	}
	PrewarmImages(ctx, message(dropped))
	value, err := cache.prepareAttachment(ctx, &MediaPart{Source: src(foreground)}, false)
	close(release)
	if err != nil || len(value.images) != 1 {
		t.Fatalf("foreground waited for warming: images=%d err=%v", len(value.images), err)
	}
}
