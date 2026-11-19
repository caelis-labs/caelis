package gatewayapp

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/workspacetrust"
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
		var applied, pending [4]mcpConfigFileStamp
		var appliedOnce bool
		var retryAt time.Time
		var retryDelay time.Duration
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				watchPaths := paths
				if workspacetrust.Lookup(trust, s.workspace.CWD) != workspacetrust.Trusted {
					watchPaths[2], watchPaths[3] = "", ""
				}
				current := mcpConfigFileStamps(watchPaths)
				if appliedOnce && retryDelay == 0 && current == applied {
					continue
				}
				if current != pending || retryDelay == 0 {
					pending, retryAt, retryDelay = current, time.Time{}, mcpConfigPollInterval
				}
				if now.Before(retryAt) {
					continue
				}
				doc, loadErr := s.authorities.store.LoadContext(ctx)
				if loadErr == nil {
					servers, trust = doc.MCPServers, doc.WorkspaceTrust
				}
				if ctx.Err() != nil {
					return
				}
				specs, err := resolveConfiguredMCPServers(servers, trust, s.workspace.CWD)
				if err == nil {
					err = apply(specs)
				}
				if err = errors.Join(loadErr, err); err == nil {
					applied, appliedOnce, retryDelay = current, true, 0
				} else {
					retryAt = now.Add(retryDelay)
					retryDelay = min(2*retryDelay, 15*time.Second)
					if s.authorities.diagnostics != nil {
						s.authorities.diagnostics.WarnContext(ctx, "MCP configuration reload will retry", "error", err)
					}
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
	digest   [sha256.Size]byte
	readable bool
}

func mcpConfigFileStamps(paths [4]string) [4]mcpConfigFileStamp {
	var stamps [4]mcpConfigFileStamp
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		digest := sha256.New()
		_, err = io.Copy(digest, file)
		_ = file.Close()
		if err == nil {
			stamps[i] = mcpConfigFileStamp{digest: [sha256.Size]byte(digest.Sum(nil)), readable: true}
		}
	}
	return stamps
}
