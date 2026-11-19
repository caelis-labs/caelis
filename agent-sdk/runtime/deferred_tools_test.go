package runtime

import (
	"slices"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type runtimeDeferredSource struct {
	mu    sync.Mutex
	ready []tool.Tool
}

func (s *runtimeDeferredSource) Tools() []tool.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ready)
}
func (s *runtimeDeferredSource) publish(t tool.Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = []tool.Tool{t}
}
