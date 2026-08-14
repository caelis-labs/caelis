package plugin

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

type managedPluginCacheGate struct {
	token chan struct{}
	refs  int
}

var managedPluginCacheOperations = struct {
	sync.Mutex
	gates map[string]*managedPluginCacheGate
}{
	gates: make(map[string]*managedPluginCacheGate),
}

// acquireManagedPluginCacheOperation serializes effects for one deterministic
// managed install root without blocking unrelated plugin caches.
func acquireManagedPluginCacheOperation(ctx context.Context, root string) (func(), error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return func() {}, nil
	}
	key, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	key = filepath.Clean(key)
	if ctx == nil {
		ctx = context.Background()
	}

	managedPluginCacheOperations.Lock()
	gate := managedPluginCacheOperations.gates[key]
	if gate == nil {
		gate = &managedPluginCacheGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		managedPluginCacheOperations.gates[key] = gate
	}
	gate.refs++
	managedPluginCacheOperations.Unlock()

	select {
	case <-ctx.Done():
		releaseManagedPluginCacheGateRef(key, gate)
		return nil, ctx.Err()
	case <-gate.token:
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			gate.token <- struct{}{}
			releaseManagedPluginCacheGateRef(key, gate)
		})
	}, nil
}

func releaseManagedPluginCacheGateRef(key string, gate *managedPluginCacheGate) {
	managedPluginCacheOperations.Lock()
	defer managedPluginCacheOperations.Unlock()
	gate.refs--
	if gate.refs == 0 && managedPluginCacheOperations.gates[key] == gate {
		delete(managedPluginCacheOperations.gates, key)
	}
}
