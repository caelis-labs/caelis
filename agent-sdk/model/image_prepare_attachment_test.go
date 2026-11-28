package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
		value, err := cache.prepareAttachment(ctx, &MediaPart{Source: MediaSource{Kind: MediaSourceAttachment, Attachment: ref}})
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

func warmFixtureRef(t *testing.T) (AttachmentRef, []byte) {
	t.Helper()
	pixel := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, pixel); err != nil {
		t.Fatal(err)
	}
	raw := pngBytes.Bytes()
	return AttachmentRef{SessionID: "session", Instance: "small", Digest: fmt.Sprintf("%x", sha256.Sum256(raw)), Size: int64(len(raw)), Width: 2, Height: 2}, raw
}

func warmFixtureMedia(ref AttachmentRef) MediaPart {
	return MediaPart{Modality: MediaModalityImage, Source: MediaSource{Kind: MediaSourceAttachment, Attachment: ref}, MimeType: "image/png"}
}

func warmFixtureMessage(ref AttachmentRef) *Message {
	part := warmFixtureMedia(ref)
	msg := NewMessage(RoleUser, NewMediaPart(MediaModalityImage, part.Source, part.MimeType, "picture"))
	return &msg
}

func waitForImageTest(t *testing.T, ctx context.Context, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("image preparation did not reach the expected boundary")
	}
}

type countedAttachmentReader struct {
	io.Reader
	start func()
}

func (r *countedAttachmentReader) Read(p []byte) (int, error) {
	if start := r.start; start != nil {
		r.start = nil
		start()
	}
	return r.Reader.Read(p)
}

func TestAttachmentPrewarmSkipsLargeAndSharesSameKeyWithForeground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		small, raw := warmFixtureRef(t)
		sourceTooBig, pixelTooBig := small, small
		sourceTooBig.Instance, sourceTooBig.Size = "bytes", imageWarmSourceBytes+1
		pixelTooBig.Instance, pixelTooBig.Height = "pixels", imageWarmEdge+1
		warmOpened, demandOpened, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var opens, reads atomic.Int32
		var warmReleased atomic.Bool
		reader := attachmentReaderFunc(func(ctx context.Context, _ string, ref AttachmentRef) (io.ReadCloser, error) {
			if ref != small {
				t.Error("large image reached warm I/O")
				return nil, ErrAttachmentUnavailable
			}
			switch opens.Add(1) {
			case 1:
				close(warmOpened)
				select {
				case <-release:
					warmReleased.Store(true)
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			case 2:
				close(demandOpened)
			}
			return io.NopCloser(&countedAttachmentReader{Reader: bytes.NewReader(raw), start: func() { reads.Add(1) }}), nil
		})
		cache := newImagePreparationCache(1<<20, 4, time.Minute)
		defer cache.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		ctx = WithImageAttachments(ctx, small.SessionID, reader, nil, cache)
		PrewarmImages(ctx, warmFixtureMessage(sourceTooBig))
		PrewarmImages(ctx, warmFixtureMessage(pixelTooBig))
		PrewarmImages(ctx, warmFixtureMessage(small))
		waitForImageTest(t, ctx, warmOpened)
		done := make(chan struct{})
		go func() {
			defer close(done)
			part := warmFixtureMedia(small)
			got, err := cache.prepareAttachment(ctx, &part)
			if err != nil || len(got.images) != 1 {
				t.Errorf("foreground image was unavailable: %v", err)
			}
		}()
		waitForImageTest(t, ctx, demandOpened)
		synctest.Wait() // the same-key request joins the held warm preparation
		unrelatedDone := make(chan struct{})
		go func() {
			defer close(unrelatedDone)
			_, err := cache.prepare(ctx, "unrelated demand", func() preparedImage { return preparedImage{} })
			if err != nil {
				t.Errorf("unrelated demand failed: %v", err)
			}
		}()
		synctest.Wait()
		close(release)
		waitForImageTest(t, ctx, done)
		waitForImageTest(t, ctx, unrelatedDone)
		if opens.Load() != 2 || reads.Load() != 1 || !warmReleased.Load() {
			t.Fatalf("authorizations=%d body reads=%d joined work completed=%v", opens.Load(), reads.Load(), warmReleased.Load())
		}
	})
}

func TestAttachmentPrewarmYieldsToUnrelatedDemandAndCloses(t *testing.T) {
	small, raw := warmFixtureRef(t)
	foreground, competing, teardown := small, small, small
	foreground.Instance, competing.Instance, teardown.Instance = "foreground", "competing", "teardown"
	warmEntered, warmCanceled := make(chan struct{}), make(chan struct{})
	fgReading, fgRelease := make(chan struct{}), make(chan struct{})
	teardownEntered, teardownCanceled, teardownRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	reader := attachmentReaderFunc(func(ctx context.Context, _ string, ref AttachmentRef) (io.ReadCloser, error) {
		switch ref {
		case small:
			close(warmEntered)
			<-ctx.Done()
			close(warmCanceled)
			return nil, ctx.Err()
		case foreground:
			return io.NopCloser(&countedAttachmentReader{Reader: bytes.NewReader(raw), start: func() {
				close(fgReading)
				select {
				case <-fgRelease:
				case <-ctx.Done():
				}
			}}), nil
		case teardown:
			close(teardownEntered)
			<-ctx.Done()
			close(teardownCanceled)
			<-teardownRelease
			return nil, ctx.Err()
		default:
			t.Error("busy or closed cache launched a warm job")
			return nil, ErrAttachmentUnavailable
		}
	})
	cache := newImagePreparationCache(1<<20, 4, time.Minute)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer close(teardownRelease)
	defer cancel()
	ctx = WithImageAttachments(ctx, small.SessionID, reader, nil, cache)
	PrewarmImages(ctx, warmFixtureMessage(small))
	waitForImageTest(t, ctx, warmEntered)
	done := make(chan struct{})
	go func() {
		defer close(done)
		part := warmFixtureMedia(foreground)
		got, err := cache.prepareAttachment(ctx, &part)
		if err != nil || len(got.images) != 1 {
			t.Errorf("foreground preparation failed: %v", err)
		}
	}()
	waitForImageTest(t, ctx, warmCanceled)
	waitForImageTest(t, ctx, fgReading)
	PrewarmImages(ctx, warmFixtureMessage(competing))
	close(fgRelease)
	waitForImageTest(t, ctx, done)
	PrewarmImages(ctx, warmFixtureMessage(teardown))
	waitForImageTest(t, ctx, teardownEntered)
	stopped := make(chan struct{})
	go func() { cache.Close(); close(stopped) }()
	waitForImageTest(t, ctx, teardownCanceled)
	select {
	case <-stopped:
		t.Fatal("Close returned before the warm reader exited")
	default:
	}
	// Let the worker finish while the parent Run context is still alive.
	teardownRelease <- struct{}{}
	waitForImageTest(t, ctx, stopped)
	PrewarmImages(ctx, warmFixtureMessage(competing))
	if ctx.Err() != nil || cache.lru.Len() != 0 || cache.bytes != 0 {
		t.Fatal("Close did not empty the Host cache independently of the Run")
	}
}
