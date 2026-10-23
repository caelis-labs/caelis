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

// This process-local cache contains disposable derivatives keyed only by source
// content. It never owns Session history or raw decoded images. Validation-only
// entries retain a MIME result, not source bytes. LRU eviction bounds retained
// bytes and entry count; expiry releases inactive entries even without new work.
// Serializing cache misses bounds concurrent decode/encode working memory.
type imagePreparationCache struct {
	mu                     sync.Mutex
	entries                map[[sha256.Size]byte]*list.Element
	lru                    list.List
	bytes, maxBytes, limit int
	ttl                    time.Duration
	processing             chan struct{}
}

type imagePreparationEntry struct {
	key     [sha256.Size]byte
	value   preparedImage
	bytes   int
	expires time.Time
	timer   *time.Timer
}

func newImagePreparationCache(maxBytes, limit int, ttl time.Duration) *imagePreparationCache {
	return &imagePreparationCache{
		entries: make(map[[sha256.Size]byte]*list.Element), maxBytes: maxBytes,
		limit: limit, ttl: ttl, processing: make(chan struct{}, 1),
	}
}

func (c *imagePreparationCache) prepare(ctx context.Context, source string, load func() preparedImage) (preparedImage, error) {
	if err := ctx.Err(); err != nil {
		return preparedImage{}, err
	}
	// Oversized inputs are rejected without hashing or decoding them.
	if len(source) > base64.StdEncoding.EncodedLen(imageMaxSourceBytes) {
		return load(), nil
	}
	key := imagePreparationKey(source)
	if value, ok := c.get(key); ok {
		return value, nil
	}
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
	value := load()
	if err := ctx.Err(); err != nil {
		return preparedImage{}, err
	}
	c.put(key, value)
	return value, nil
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

func (c *imagePreparationCache) get(key [sha256.Size]byte) (preparedImage, bool) {
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

func (c *imagePreparationCache) put(key [sha256.Size]byte, value preparedImage) {
	size := 256 + len(value.note)
	for _, part := range value.images {
		size += 64 + len(part.data) + len(part.mimeType) + len(part.note)
	}
	if size > c.maxBytes || c.limit <= 0 || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
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

func (c *imagePreparationCache) remove(element *list.Element) {
	entry := element.Value.(*imagePreparationEntry)
	delete(c.entries, entry.key)
	c.lru.Remove(element)
	c.bytes -= entry.bytes
	if entry.timer != nil {
		entry.timer.Stop()
	}
}
