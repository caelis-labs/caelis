package gatewayapp

import (
	"strings"
	"sync"
)

// runtimeProcessConfigSource owns mutable process configuration sampled once
// for each Session Runtime activation. It is independent from the Host root
// Runtime composition so assemblers do not retain that composition indirectly.
type runtimeProcessConfigSource struct {
	mu sync.RWMutex

	runtime               stackRuntimeConfig
	sandboxOverride       SandboxConfig
	childControlURL       string
	childControlTokenFile string
}

func newRuntimeProcessConfigSource(snapshot sessionRuntimeProcessSnapshot) *runtimeProcessConfigSource {
	return &runtimeProcessConfigSource{
		runtime:               cloneSessionRuntimeConfig(snapshot.runtime),
		sandboxOverride:       cloneSandboxConfig(snapshot.sandboxOverride),
		childControlURL:       strings.TrimSpace(snapshot.childControlURL),
		childControlTokenFile: strings.TrimSpace(snapshot.childControlTokenFile),
	}
}

func (s *runtimeProcessConfigSource) snapshot() sessionRuntimeProcessSnapshot {
	if s == nil {
		return sessionRuntimeProcessSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sessionRuntimeProcessSnapshot{
		runtime:               cloneSessionRuntimeConfig(s.runtime),
		sandboxOverride:       cloneSandboxConfig(s.sandboxOverride),
		childControlURL:       s.childControlURL,
		childControlTokenFile: s.childControlTokenFile,
	}
}

func (s *runtimeProcessConfigSource) setRuntime(runtime stackRuntimeConfig) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.runtime = cloneSessionRuntimeConfig(runtime)
	s.mu.Unlock()
}

func (s *runtimeProcessConfigSource) setChildControl(controlURL string, tokenFile string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.childControlURL = strings.TrimSpace(controlURL)
	s.childControlTokenFile = strings.TrimSpace(tokenFile)
	s.mu.Unlock()
}
