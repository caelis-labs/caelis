// Package history provides Control replay indexes and bounded display
// projections. Source records remain authoritative for history and model context.
package history

import (
	"sort"
	"sync"
)

const DefaultTurns = 16
const MaxTurns = 64

// Index is guarded by its caller's source lock. High is the first unread source
// position. Positions can be spool offsets or sequence boundaries.
type Index struct {
	High  uint64
	turns []turn
	byID  map[string]int
	last  string
}
type turn struct {
	first       uint64
	occurrences []uint64
}

// Observe indexes a record at position. Empty identities are attached to the
// surrounding history, never guessed into synthetic Turns.
func (i *Index) Observe(position uint64, id string) {
	if id == "" || id == i.last {
		return
	}
	if i.byID == nil {
		i.byID = map[string]int{}
	}
	n, ok := i.byID[id]
	if !ok {
		n = len(i.turns)
		i.byID[id] = n
		i.turns = append(i.turns, turn{first: position})
	}
	i.turns[n].occurrences = append(i.turns[n].occurrences, position)
	i.last = id
}

// Start returns a complete-Turn suffix strictly before before. If a Turn
// interleaves with another, the window expands to retain its entire prefix.
// Reads at an older boundary are unaffected by later appended observations.
func (i *Index) Start(before uint64, count int) uint64 {
	if count <= 0 {
		return 0
	}
	count = min(count, MaxTurns)
	end := sort.Search(len(i.turns), func(n int) bool { return i.turns[n].first >= before })
	if end <= count {
		return 0
	}
	start := i.turns[end-count].first
	for n := end - count - 1; n >= 0; n-- {
		positions := i.turns[n].occurrences
		p := sort.Search(len(positions), func(p int) bool { return positions[p] >= before })
		if p > 0 && positions[p-1] >= start {
			start = i.turns[n].first
		}
	}
	return start
}

// Entry serializes incremental indexing for one immutable source incarnation.
type Entry struct {
	sync.Mutex
	Index Index
}

// Cache bounds rebuildable in-memory indexes across independently owned Tasks.
// Eviction cannot invalidate issued tokens: a reader can reconstruct the index.
type Cache struct {
	mu      sync.Mutex
	entries map[string]*Entry
	order   []string
}

func (c *Cache) Get(key string) *Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[key]; e != nil {
		return e
	}
	if c.entries == nil {
		c.entries = map[string]*Entry{}
	}
	if len(c.order) >= 32 {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
	e := &Entry{}
	c.entries[key] = e
	c.order = append(c.order, key)
	return e
}
