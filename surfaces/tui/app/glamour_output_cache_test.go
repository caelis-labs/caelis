package tuiapp

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestGlamourOutputCacheMatchesFreshRender(t *testing.T) {
	clearGlamourCache()
	sources := []string{
		"## Heading\n\nUse `code` and [link](https://example.com).",
		"普通中文 **加粗** 与 `go test`。",
		"```go\nfunc main() {}\n```",
	}
	themes := []tuikit.Theme{
		tuikit.ResolveSelectedTheme("nord", nil, true, false, colorprofile.TrueColor),
		tuikit.ResolveSelectedTheme("catppuccin-latte", nil, false, false, colorprofile.TrueColor),
	}
	roles := []tuikit.LineStyle{tuikit.LineStyleAssistant, tuikit.LineStyleReasoning}
	for _, raw := range sources {
		for _, theme := range themes {
			for _, role := range roles {
				for _, width := range []int{72, 96} {
					clearGlamourCache()
					fresh := glamourRenderNarrative(raw, width, theme, role)
					if _, hit := defaultGlamourOutputCache.lookup(raw, width, themeRenderCacheKey(theme), role); !hit {
						t.Fatal("Glamour owner did not retain its rendered output")
					}
					cached := glamourRenderNarrative(raw, width, theme, role)
					if fresh == "" || cached != fresh {
						t.Fatalf("cached output diverged from fresh render theme=%s role=%v width=%d", theme.Name, role, width)
					}
					if ansi.Strip(cached) == "" {
						t.Fatal("cached render dropped markdown text")
					}
				}
			}
		}
	}
}

func TestGlamourOutputCacheSeparatesThemeRoleWidthAndSource(t *testing.T) {
	clearGlamourCache()
	nord := tuikit.ResolveSelectedTheme("nord", nil, true, false, colorprofile.TrueColor)
	latte := tuikit.ResolveSelectedTheme("catppuccin-latte", nil, false, false, colorprofile.TrueColor)
	raw := "## Heading\n\nUse `code` and [link](https://example.com)."
	nordWide := glamourRenderNarrative(raw, 96, nord, tuikit.LineStyleAssistant)
	nordNarrow := glamourRenderNarrative(raw, 72, nord, tuikit.LineStyleAssistant)
	latteWide := glamourRenderNarrative(raw, 96, latte, tuikit.LineStyleAssistant)
	reason := glamourRenderNarrative(raw, 96, nord, tuikit.LineStyleReasoning)
	other := glamourRenderNarrative(raw+"\n\nMore.", 96, nord, tuikit.LineStyleAssistant)
	if nordWide == nordNarrow || nordWide == latteWide || nordWide == reason || nordWide == other {
		t.Fatal("cache mixed theme, role, width, or source")
	}
	if ansi.Strip(nordWide) == ansi.Strip(other) {
		t.Fatal("different source produced the same text")
	}
	if again := glamourRenderNarrative(raw, 96, nord, tuikit.LineStyleAssistant); again != nordWide {
		t.Fatal("returning to the original key used a stale render")
	}
}

func TestGlamourOutputCacheEvictsByBytesAndEntries(t *testing.T) {
	for _, limits := range [][2]int{{240, 3}, {80, 8}} {
		t.Run(fmt.Sprint(limits), func(t *testing.T) {
			t.Parallel()
			cache := newGlamourOutputCache(limits[0], limits[1])
			render := func(raw string) string { return strings.Repeat("R", 20) + raw }
			for i := range 8 {
				raw := fmt.Sprintf("src-%d", i)
				got, hit, err := cache.getOrRender(raw, 80, "nord", tuikit.LineStyleAssistant, func() (string, error) {
					return render(raw), nil
				})
				if err != nil || hit || got != render(raw) {
					t.Fatalf("store %d: got=%q hit=%t err=%v", i, got, hit, err)
				}
			}
			entries, bytes := cache.stats()
			wantEntries := min(8, limits[1], limits[0]/glamourOutputCacheCost("src-0", "nord", render("src-0")))
			if entries != wantEntries || bytes > limits[0] {
				t.Fatalf("entries=%d bytes=%d, want entries=%d within byte cap %d", entries, bytes, wantEntries, limits[0])
			}
			if _, hit, _ := cache.getOrRender("src-0", 80, "nord", tuikit.LineStyleAssistant, func() (string, error) {
				return render("src-0"), nil
			}); hit {
				t.Fatal("oldest entry survived eviction")
			}
		})
	}
}

func TestGlamourOutputCacheBypassesOversizeEntries(t *testing.T) {
	t.Parallel()
	cache := newGlamourOutputCache(32, 8)
	raw := strings.Repeat("source-", 8)
	rendered, hit, err := cache.getOrRender(raw, 80, "nord", tuikit.LineStyleAssistant, func() (string, error) {
		return strings.Repeat("out", 20), nil
	})
	if err != nil || hit || rendered == "" {
		t.Fatalf("oversize render failed: hit=%t err=%v", hit, err)
	}
	if entries, bytes := cache.stats(); entries != 0 || bytes != 0 {
		t.Fatalf("oversize entry was retained: entries=%d bytes=%d", entries, bytes)
	}
}

func TestGlamourOutputCacheKeepsRecentEntriesAndRejectsErrors(t *testing.T) {
	t.Parallel()
	cache := newGlamourOutputCache(1024, 2)
	for _, raw := range []string{"a", "b"} {
		cache.store(raw, 80, "nord", tuikit.LineStyleAssistant, "rendered:"+raw)
	}
	cache.lookup("a", 80, "nord", tuikit.LineStyleAssistant)
	cache.store("c", 80, "nord", tuikit.LineStyleAssistant, "rendered:c")
	if _, ok := cache.lookup("a", 80, "nord", tuikit.LineStyleAssistant); !ok {
		t.Fatal("recently used entry was evicted")
	}
	if _, ok := cache.lookup("b", 80, "nord", tuikit.LineStyleAssistant); ok {
		t.Fatal("least recently used entry was retained")
	}
	wantErr := errors.New("render failed")
	for range 2 {
		_, hit, err := cache.getOrRender("error", 80, "nord", tuikit.LineStyleAssistant, func() (string, error) {
			return "partial render", wantErr
		})
		if hit || !errors.Is(err, wantErr) {
			t.Fatal("failed render was cached")
		}
	}
}

func TestGlamourOutputCacheConcurrentAccess(t *testing.T) {
	t.Parallel()
	cache := newGlamourOutputCache(64<<10, 32)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 40 {
				raw := fmt.Sprintf("src-%d", j%6)
				theme := fmt.Sprintf("theme-%d", i%3)
				want := theme + ":" + raw
				got, _, err := cache.getOrRender(raw, 80+i%2, theme, tuikit.LineStyleAssistant, func() (string, error) {
					return strings.Clone(want), nil
				})
				if err != nil || got != want {
					t.Errorf("concurrent getOrRender = %q, want %q err=%v", got, want, err)
				}
			}
		}(i)
	}
	wg.Wait()
	entries, bytes := cache.stats()
	if entries == 0 || bytes <= 0 || entries > 32 || bytes > 64<<10 {
		t.Fatalf("concurrent cache stats entries=%d bytes=%d", entries, bytes)
	}
}

func clearGlamourCache() {
	glamourCache.Lock()
	glamourCache.entries = nil
	glamourCache.order = nil
	glamourCache.Unlock()
	glamourStreamingCache.Lock()
	glamourStreamingCache.entries = nil
	glamourStreamingCache.order = nil
	glamourStreamingCache.Unlock()
	defaultGlamourOutputCache.clear()
}

func (c *glamourOutputCache) clear() {
	c.mu.Lock()
	c.entries = make(map[glamourOutputCacheKey]string)
	c.order = nil
	c.bytes = 0
	c.mu.Unlock()
}

func (c *glamourOutputCache) stats() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.bytes
}
