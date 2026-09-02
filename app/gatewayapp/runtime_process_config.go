package gatewayapp

import (
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/control/memorybinding"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
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
	memorySelection       memorybinding.RuntimeSelection
	memorySelector        MemoryBindingSelector
}

func newRuntimeProcessConfigSource(snapshot sessionRuntimeProcessSnapshot) *runtimeProcessConfigSource {
	return &runtimeProcessConfigSource{
		runtime:               cloneSessionRuntimeConfig(snapshot.runtime),
		sandboxOverride:       cloneSandboxConfig(snapshot.sandboxOverride),
		childControlURL:       strings.TrimSpace(snapshot.childControlURL),
		childControlTokenFile: strings.TrimSpace(snapshot.childControlTokenFile),
		memorySelection: memorybinding.RuntimeSelection{
			BindingRef: memorybinding.BindingRef(strings.TrimSpace(string(snapshot.memorySelection.BindingRef))),
		},
		memorySelector: snapshot.memorySelector,
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
		memorySelection:       s.memorySelection,
		memorySelector:        s.memorySelector,
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

func (s *runtimeProcessConfigSource) setSandboxOverride(config SandboxConfig) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sandboxOverride = cloneSandboxConfig(config)
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

// runtimeProcessSnapshot returns the one mutable Host process configuration,
// or the immutable activation snapshot for a detached Session Runtime. It
// never holds composition and process-source locks at the same time.
func (s *runtimeComposition) runtimeProcessSnapshot() sessionRuntimeProcessSnapshot {
	if s == nil {
		return sessionRuntimeProcessSnapshot{}
	}
	if s.process != nil && s.process.config != nil {
		return s.process.config.snapshot()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	activation := s.activation
	if activation == nil {
		activation = &sessionRuntimeActivation{}
	}
	return sessionRuntimeProcessSnapshot{
		runtime:               cloneActiveRuntimeConfig(s.activeRuntime),
		childControlURL:       activation.childControlURL,
		childControlTokenFile: activation.childControlTokenFile,
	}
}

func cloneActiveRuntimeConfig(config stackRuntimeConfig) stackRuntimeConfig {
	config.Model = cloneSessionModelConfig(config.Model)
	config.SkillDirs = cloneStringSlicePreserveNil(config.SkillDirs)
	config.PluginSkills = skill.ClonePluginBundles(config.PluginSkills)
	config.SkillCatalog = skill.NewCatalog(config.SkillCatalog.Metas())
	config.Plugins = clonePluginConfigs(config.Plugins)
	config.BaseAssembly = assembly.CloneResolvedAssembly(config.BaseAssembly)
	config.Assembly = assembly.CloneResolvedAssembly(config.Assembly)
	config.BaseMetadata = cloneMap(config.BaseMetadata)
	return config
}
