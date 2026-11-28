package model

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"
)

var sharedImagePreparationCache = newImagePreparationCache(64*1024*1024, 128, 5*time.Minute)

// ImagePreparationCache keeps bounded, disposable model image preparations.
// Inline validation entries retain only their disposition. Attachment entries
// can retain encoded provider bytes so hot requests avoid reading the source.
type ImagePreparationCache struct {
	mu                     sync.Mutex
	entries                map[[sha256.Size]byte]*list.Element
	lru                    list.List
	bytes, maxBytes, limit int
	ttl                    time.Duration
	processing             chan struct{}
	warmKey                [sha256.Size]byte
	warmJoined             bool
	cancelWarm             context.CancelFunc
	warmDone               chan struct{}
	waiting                int
	closed                 bool
}

type imagePreparationEntry struct {
	key     [sha256.Size]byte
	value   preparedImage
	bytes   int
	expires time.Time
	timer   *time.Timer
}

// NewImagePreparationCache creates a Host-local cache with production bounds.
func NewImagePreparationCache() *ImagePreparationCache {
	return newImagePreparationCache(64*1024*1024, 128, 5*time.Minute)
}

func newImagePreparationCache(maxBytes, limit int, ttl time.Duration) *ImagePreparationCache {
	return &ImagePreparationCache{
		entries: make(map[[sha256.Size]byte]*list.Element), maxBytes: maxBytes,
		limit: limit, ttl: ttl, processing: make(chan struct{}, 1),
	}
}

func (c *ImagePreparationCache) prepare(ctx context.Context, source string, load func() preparedImage) (preparedImage, error) {
	if err := ctx.Err(); err != nil {
		return preparedImage{}, err
	}
	// Oversized inputs are rejected without hashing or decoding them.
	if len(source) > base64.StdEncoding.EncodedLen(imageMaxSourceBytes) {
		return load(), nil
	}
	return c.prepareKey(ctx, imagePreparationKey(source), func() (preparedImage, error) { return load(), nil })
}

func (c *ImagePreparationCache) prepareKey(ctx context.Context, key [sha256.Size]byte, load func() (preparedImage, error)) (preparedImage, error) {
	if value, ok := c.get(key); ok {
		return value, nil
	}
	c.mu.Lock()
	c.waiting++
	if c.cancelWarm != nil {
		if c.warmKey == key {
			c.warmJoined = true
		} else if !c.warmJoined {
			c.cancelWarm()
		}
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.waiting--
		c.mu.Unlock()
	}()
	select {
	case c.processing <- struct{}{}:
		defer func() { <-c.processing }()
	case <-ctx.Done():
		return preparedImage{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return preparedImage{}, err
	}
	// Another invocation may have prepared this content while we waited.
	if value, ok := c.get(key); ok {
		return value, nil
	}
	value, err := load()
	if err != nil {
		return preparedImage{}, err
	}
	if err := ctx.Err(); err != nil {
		return preparedImage{}, err
	}
	c.put(key, value)
	return value, nil
}

// prewarm reserves the same lane as demand before starting a goroutine. An
// unrelated demand can cancel it until a foreground request joins that load.
func (c *ImagePreparationCache) prewarm(ctx context.Context, key [sha256.Size]byte, load func(context.Context) (preparedImage, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.waiting != 0 || c.entries[key] != nil {
		return
	}
	select {
	case c.processing <- struct{}{}:
	default:
		return
	}
	// Derive from the Run and allow Host teardown or foreground demand to stop
	// I/O even while the Run itself is still active.
	warmCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.warmKey, c.cancelWarm, c.warmDone = key, cancel, done
	go func() {
		defer func() {
			cancel()
			c.mu.Lock()
			c.cancelWarm, c.warmDone, c.warmJoined = nil, nil, false
			<-c.processing
			close(done)
			c.mu.Unlock()
		}()
		if result, err := load(warmCtx); err == nil && warmCtx.Err() == nil {
			c.put(key, result)
		}
	}()
}

// Close cancels and joins opportunistic work and releases cached preparations.
// Its Host calls Close after draining foreground producers.
func (c *ImagePreparationCache) Close() {
	c.mu.Lock()
	c.closed = true
	done := c.warmDone
	if c.cancelWarm != nil {
		c.cancelWarm()
	}
	for c.lru.Len() > 0 {
		c.remove(c.lru.Back())
	}
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Hash in fixed-size chunks: converting a whole base64 string to []byte would
// allocate another copy of every historical image even on a cache hit.
func imagePreparationKey(source string) [sha256.Size]byte {
	hash := sha256.New()
	var chunk [4096]byte
	for len(source) != 0 {
		n := copy(chunk[:], source)
		_, _ = hash.Write(chunk[:n])
		source = source[n:]
	}
	var key [sha256.Size]byte
	hash.Sum(key[:0])
	return key
}

func (c *ImagePreparationCache) get(key [sha256.Size]byte) (preparedImage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.entries[key]
	if element == nil {
		return preparedImage{}, false
	}
	entry := element.Value.(*imagePreparationEntry)
	if !time.Now().Before(entry.expires) {
		c.remove(element)
		return preparedImage{}, false
	}
	c.lru.MoveToFront(element)
	return entry.value, true
}

func (c *ImagePreparationCache) put(key [sha256.Size]byte, value preparedImage) {
	size := 256 + len(value.note)
	for _, part := range value.images {
		size += 64 + len(part.data) + len(part.mimeType) + len(part.note)
	}
	if size > c.maxBytes || c.limit <= 0 || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if previous := c.entries[key]; previous != nil {
		c.remove(previous)
	}
	for c.bytes+size > c.maxBytes || c.lru.Len() >= c.limit {
		c.remove(c.lru.Back())
	}
	entry := &imagePreparationEntry{key: key, value: value, bytes: size, expires: time.Now().Add(c.ttl)}
	element := c.lru.PushFront(entry)
	c.entries[key] = element
	c.bytes += size
	entry.timer = time.AfterFunc(c.ttl, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.entries[key] == element {
			c.remove(element)
		}
	})
}

func (c *ImagePreparationCache) remove(element *list.Element) {
	entry := element.Value.(*imagePreparationEntry)
	delete(c.entries, entry.key)
	c.lru.Remove(element)
	c.bytes -= entry.bytes
	if entry.timer != nil {
		entry.timer.Stop()
	}
}
