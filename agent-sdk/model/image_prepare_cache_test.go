package model

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func clearImagePreparationCache(c *imagePreparationCache) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.lru.Len() != 0 {
		c.remove(c.lru.Back())
	}
}

func TestImagePreparationCacheSharesValidationAcrossTurnsAndConcurrentRetries(t *testing.T) {
	cache := newImagePreparationCache(1<<20, 32, time.Minute)
	t.Cleanup(func() { clearImagePreparationCache(cache) })
	var loads atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			value, err := cache.prepare(t.Context(), "same source", func() preparedImage {
				loads.Add(1)
				return preparedImage{images: []preparedImagePart{{mimeType: "image/png"}}}
			})
			if err != nil || len(value.images) != 1 {
				t.Errorf("cached preparation failed: %v", err)
			}
		})
	}
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("source decoded %d times, want once", loads.Load())
	}
	// A changed source must be validated even when its name/metadata are the same.
	if _, err := cache.prepare(t.Context(), "different source", func() preparedImage {
		loads.Add(1)
		return preparedImage{note: "omitted: invalid image"}
	}); err != nil || loads.Load() != 2 {
		t.Fatalf("changed source reused a stale derivative: loads=%d err=%v", loads.Load(), err)
	}
}

func TestImagePreparationCacheEvictsByBytesAndRecency(t *testing.T) {
	cache := newImagePreparationCache(1100, 2, time.Minute)
	t.Cleanup(func() { clearImagePreparationCache(cache) })
	load := func() preparedImage {
		return preparedImage{images: []preparedImagePart{{data: strings.Repeat("x", 100)}}}
	}
	for _, source := range []string{"one", "two", "one", "three"} {
		if _, err := cache.prepare(t.Context(), source, load); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := cache.get(sha256.Sum256([]byte("two"))); ok {
		t.Fatal("least recently used derivative was retained")
	}
	if _, ok := cache.get(sha256.Sum256([]byte("one"))); !ok {
		t.Fatal("recent derivative was evicted")
	}
	// The byte budget also evicts even when the entry-count limit is not hit.
	cache.limit = 100
	if _, err := cache.prepare(t.Context(), "four", load); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.bytes > cache.maxBytes || cache.lru.Len() != 2 {
		t.Fatalf("unbounded cache: bytes=%d entries=%d", cache.bytes, cache.lru.Len())
	}
}

func TestImagePreparationCacheReleasesExpiredDerivativesWithoutTraffic(t *testing.T) {
	cache := newImagePreparationCache(1<<20, 10, 10*time.Millisecond)
	t.Cleanup(func() { clearImagePreparationCache(cache) })
	if _, err := cache.prepare(t.Context(), "source", func() preparedImage { return preparedImage{note: "test"} }); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		cache.mu.Lock()
		empty := len(cache.entries) == 0 && cache.bytes == 0
		cache.mu.Unlock()
		if empty {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("expired derivatives retained without new traffic")
		}
	}
}

func TestImagePreparationCacheWaitingMissHonorsCancellation(t *testing.T) {
	cache := newImagePreparationCache(1<<20, 10, time.Minute)
	cache.processing <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := cache.prepare(ctx, "source", func() preparedImage {
		t.Error("cancelled cache miss decoded an image")
		return preparedImage{}
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestImagePreparationHistoryCacheRetainsNoOriginalBytes(t *testing.T) {
	cache := newImagePreparationCache(1<<20, 32, time.Minute)
	t.Cleanup(func() { clearImagePreparationCache(cache) })
	part := imageTestPart(t, 1024, 1024)
	for _, name := range []string{"private-a.png", "private-b.png"} {
		req := &Request{Messages: []Message{NewMessage(RoleUser, part)}}
		req.Messages[0].Parts[0].Media.Name = name
		if err := cache.prepareRequest(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		if req.Messages[0].Parts[0].Media.Name != name || req.Messages[0].Parts[0].Media.Source != part.Media.Source {
			t.Fatal("cache substituted original bytes or name")
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, element := range cache.entries {
		for _, part := range element.Value.(*imagePreparationEntry).value.images {
			if part.data != "" {
				t.Fatal("pass-through validation retained original media")
			}
		}
	}
}

func twentyImageHistory(t testing.TB) []Message {
	t.Helper()
	var history []Message
	for i := range 20 {
		part := imageTestPart(t, 1024, 1024)
		// A valid distinct source (PNG decoders permit trailing bytes).
		data, err := base64.StdEncoding.DecodeString(part.Media.Source.Data)
		if err != nil {
			t.Fatal(err)
		}
		part.Media.Source.Data = base64.StdEncoding.EncodeToString(append(data, byte(i)))
		history = append(history, NewMessage(RoleUser, part))
		history[i].Parts[0].Media.Name = fmt.Sprintf("shot-%d.png", i)
	}
	return history
}

// Cold and warm cases use the same twenty-image history. benchmem measures
// allocation volume; running the compiled benchmark with /usr/bin/time -l
// additionally measures process peak RSS without including compiler memory.
func BenchmarkImagePreparationTwentyImageHistory(b *testing.B) {
	history := twentyImageHistory(b)
	for _, warm := range []bool{false, true} {
		b.Run(fmt.Sprintf("warm=%v", warm), func(b *testing.B) {
			cache := newImagePreparationCache(64<<20, 128, time.Minute)
			defer clearImagePreparationCache(cache)
			if warm {
				if err := cache.prepareRequest(b.Context(), &Request{Messages: CloneMessages(history)}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if !warm {
					clearImagePreparationCache(cache)
				}
				if err := cache.prepareRequest(b.Context(), &Request{Messages: CloneMessages(history)}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestImagePreparationTwentyImageHistoryMemory(t *testing.T) {
	history := twentyImageHistory(t)
	cache := newImagePreparationCache(64<<20, 128, time.Minute)
	t.Cleanup(func() { clearImagePreparationCache(cache) })
	measure := func() (allocated, peak uint64, elapsed time.Duration) {
		t.Helper()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				var current runtime.MemStats
				runtime.ReadMemStats(&current)
				if current.HeapAlloc > before.HeapAlloc {
					peak = max(peak, current.HeapAlloc-before.HeapAlloc)
				}
				select {
				case <-ticker.C:
				case <-stop:
					return
				}
			}
		}()
		start := time.Now()
		if err := cache.prepareRequest(t.Context(), &Request{Messages: CloneMessages(history)}); err != nil {
			t.Fatal(err)
		}
		elapsed = time.Since(start)
		runtime.ReadMemStats(&after)
		close(stop)
		<-done
		if after.HeapAlloc > before.HeapAlloc {
			peak = max(peak, after.HeapAlloc-before.HeapAlloc)
		}
		return after.TotalAlloc - before.TotalAlloc, peak, elapsed
	}
	coldAlloc, coldPeak, coldTime := measure()
	warmAlloc, warmPeak, warmTime := measure()
	t.Logf("20 distinct 1024x1024 images: cold=%s allocated=%d heap_peak_delta=%d; warm=%s allocated=%d heap_peak_delta=%d", coldTime, coldAlloc, coldPeak, warmTime, warmAlloc, warmPeak)
	if warmAlloc > 1<<20 || warmPeak > 1<<20 || coldAlloc < warmAlloc*10 {
		t.Fatalf("cached history still allocates decode-sized working memory: cold=%d warm=%d peak=%d", coldAlloc, warmAlloc, warmPeak)
	}
}
