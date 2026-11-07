package runtime

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"testing"
)

// A derived name index in the fixture's existing source owner. The base source
// remains the current Tool snapshot; replacing an item is visible immediately.
type h1NamedSource struct {
	*h1Source
	positions map[string]int
}

func h1Indexed(s *h1Source) *h1NamedSource {
	out := &h1NamedSource{h1Source: s, positions: map[string]int{}}
	for i, one := range s.items {
		out.positions[one.Definition().Name] = i
	}
	return out
}
func (s *h1NamedSource) Lookup(ctx context.Context, name string) (tool.Tool, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.LookupRequests++
	i, ok := s.positions[name]
	if !ok {
		return nil, false, nil
	}
	return s.items[i], true, nil
}

func TestJSExecLookupStillFiltersAndWrapsSourceTarget(t *testing.T) {
	s := h1Directory(2)
	named := h1Indexed(s)
	wraps := 0
	wrapped := &deferredToolSource{source: named, wrap: func(items []tool.Tool) []tool.Tool { wraps++; return items }}
	if one, found, err := wrapped.Lookup(t.Context(), "a__target"); err != nil || !found || one == nil || wraps != 1 {
		t.Fatalf("current target not wrapped: %v %v %d", found, err, wraps)
	}
	s.mu.Lock()
	s.items[0] = tool.NamedTool{Def: tool.Definition{Name: "a__target"}}
	s.mu.Unlock()
	if _, found, err := wrapped.Lookup(t.Context(), "a__target"); err != nil || found || wraps != 1 {
		t.Fatalf("native injection reached wrapper: %v %v %d", found, err, wraps)
	}
	if _, found, err := wrapped.Lookup(t.Context(), "replay_alias"); err != nil || found {
		t.Fatal("missing canonical name gained alias")
	}
}
