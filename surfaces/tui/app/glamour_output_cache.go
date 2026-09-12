package tuiapp

import (
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// Rendered Markdown is cached by exact source, theme, width, and role, including
// streaming stable prefixes. Both retained string bytes and entry count are
// bounded; an oversized render is returned without being retained.
const (
	glamourOutputCacheMaxBytes   = 16 << 20
	glamourOutputCacheMaxEntries = 128
)

type glamourOutputCacheKey struct {
	width    int
	role     tuikit.LineStyle
	themeKey string
	raw      string
}

type glamourOutputCache struct {
	mu         sync.Mutex
	entries    map[glamourOutputCacheKey]string
	order      []glamourOutputCacheKey
	bytes      int
	maxBytes   int
	maxEntries int
}

func newGlamourOutputCache(maxBytes, maxEntries int) *glamourOutputCache {
	return &glamourOutputCache{
		entries:    make(map[glamourOutputCacheKey]string),
		maxBytes:   maxBytes,
		maxEntries: maxEntries,
	}
}

var defaultGlamourOutputCache = newGlamourOutputCache(glamourOutputCacheMaxBytes, glamourOutputCacheMaxEntries)

func glamourOutputCacheCost(raw, themeKey, rendered string) int {
	return len(raw) + len(themeKey) + len(rendered)
}

func (c *glamourOutputCache) lookup(raw string, width int, themeKey string, role tuikit.LineStyle) (string, bool) {
	key := glamourOutputCacheKey{width: width, role: role, themeKey: themeKey, raw: raw}
	c.mu.Lock()
	defer c.mu.Unlock()
	rendered, ok := c.entries[key]
	if !ok {
		return "", false
	}
	c.touchLocked(key)
	return rendered, true
}

func (c *glamourOutputCache) getOrRender(
	raw string,
	width int,
	themeKey string,
	role tuikit.LineStyle,
	render func() (string, error),
) (string, bool, error) {
	if rendered, ok := c.lookup(raw, width, themeKey, role); ok {
		return rendered, true, nil
	}
	rendered, err := render()
	if err != nil {
		return rendered, false, err
	}
	c.store(raw, width, themeKey, role, rendered)
	return rendered, false, nil
}

func (c *glamourOutputCache) store(raw string, width int, themeKey string, role tuikit.LineStyle, rendered string) {
	if rendered == "" {
		return
	}
	cost := glamourOutputCacheCost(raw, themeKey, rendered)
	if cost > c.maxBytes {
		return
	}
	probe := glamourOutputCacheKey{width: width, role: role, themeKey: themeKey, raw: raw}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[probe]; ok {
		c.touchLocked(probe)
		return
	}
	key := glamourOutputCacheKey{
		width:    width,
		role:     role,
		themeKey: strings.Clone(themeKey),
		raw:      strings.Clone(raw),
	}
	rendered = strings.Clone(rendered)
	for (c.bytes+cost > c.maxBytes || len(c.order) >= c.maxEntries) && len(c.order) > 0 {
		c.evictOldestLocked()
	}
	if c.bytes+cost > c.maxBytes || len(c.order) >= c.maxEntries {
		return
	}
	c.entries[key] = rendered
	c.bytes += cost
	c.order = append(c.order, key)
}

func (c *glamourOutputCache) touchLocked(key glamourOutputCacheKey) {
	for i, item := range c.order {
		if item != key {
			continue
		}
		copy(c.order[i:], c.order[i+1:])
		c.order[len(c.order)-1] = item
		return
	}
}

func (c *glamourOutputCache) evictOldestLocked() {
	evict := c.order[0]
	c.order[0] = glamourOutputCacheKey{}
	c.order = c.order[1:]
	c.bytes -= glamourOutputCacheCost(evict.raw, evict.themeKey, c.entries[evict])
	delete(c.entries, evict)
}
