package gatewayapp

import (
	"context"
	"os"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

const mcpConfigPollInterval = time.Second

// A resident Runtime watches its configuration inputs. Reloading reads the
// same sources as activation and leaves package selection to its owner.
func (s *runtimeComposition) watchNativeMCP(manager *mcp.Manager, plugins []mcp.ServerSpec) func() {
	initial, _ := s.loadGatewayAppConfig()
	return s.watchConfiguredMCP(initial, func(configured []mcp.ServerSpec) error {
		specs, err := mergeRuntimeMCPSpecs(configured, plugins)
		if err != nil {
			return err
		}
		return manager.Reconcile(specs)
	})
}

func (s *runtimeComposition) watchConfiguredMCP(initial AppConfig, apply func([]mcp.ServerSpec) error) func() {
	user, projectAgents, projectMCP := mcpConfigFiles(s.workspace.CWD)
	paths := [4]string{s.authorities.store.inner.Path(), user, projectAgents, projectMCP}
	servers, trust := initial.MCPServers, initial.WorkspaceTrust
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(mcpConfigPollInterval)
		defer ticker.Stop()
		var previous [4]mcpConfigFileStamp
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := mcpConfigFileStamps(paths)
				if current == previous {
					continue
				}
				previous = current
				if doc, err := s.authorities.store.LoadContext(ctx); err == nil {
					servers, trust = doc.MCPServers, doc.WorkspaceTrust
				} else if ctx.Err() == nil && s.authorities.diagnostics != nil {
					s.authorities.diagnostics.WarnContext(ctx, "MCP reload retained the last valid Host configuration", "error", err)
				}
				if ctx.Err() != nil {
					return
				}
				specs, err := resolveConfiguredMCPServers(servers, trust, s.workspace.CWD)
				if err == nil {
					err = apply(specs)
				}
				if err != nil && s.authorities.diagnostics != nil {
					s.authorities.diagnostics.WarnContext(ctx, "MCP configuration reload failed", "error", err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

type mcpConfigFileStamp struct {
	modified time.Time
	size     int64
}

func mcpConfigFileStamps(paths [4]string) [4]mcpConfigFileStamp {
	var stamps [4]mcpConfigFileStamp
	for i, path := range paths {
		if info, err := os.Stat(path); err == nil {
			stamps[i] = mcpConfigFileStamp{modified: info.ModTime(), size: info.Size()}
		}
	}
	return stamps
}
