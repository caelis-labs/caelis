//go:build windows

package windows

import (
	"errors"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
)

var errSandboxStateBusy = errors.New("windows sandbox state is in use")

// sandboxStateCoordinator serializes state and ACL mutation across every
// Windows sandbox Runtime in one Host that shares the same StateDir. Product
// topology permits one Host per Store, so this is the process-lifetime owner
// for Runtime-instance coordination; the Host's Store lock remains the
// cross-process authority.
type sandboxStateCoordinator struct {
	aclMu sync.Mutex

	mu             sync.Mutex
	runtimeRefs    int
	activeUses     int
	resetting      bool
	activeEnvRoots map[string]int
	envRemovals    map[string]chan struct{}
}

var sandboxStateCoordinators sync.Map

func sandboxCoordinatorFor(stateRoot string) *sandboxStateCoordinator {
	key := pathutil.Key(stateRoot)
	actual, _ := sandboxStateCoordinators.LoadOrStore(key, &sandboxStateCoordinator{
		activeEnvRoots: map[string]int{},
		envRemovals:    map[string]chan struct{}{},
	})
	return actual.(*sandboxStateCoordinator)
}

func (c *sandboxStateCoordinator) registerRuntime(envRoot string) error {
	if c == nil {
		return nil
	}
	key := pathutil.Key(envRoot)
	for {
		c.aclMu.Lock()
		c.mu.Lock()
		if c.activeEnvRoots == nil {
			c.activeEnvRoots = map[string]int{}
		}
		removal := c.envRemovals[key]
		if removal == nil {
			if c.resetting {
				c.mu.Unlock()
				c.aclMu.Unlock()
				return errSandboxStateBusy
			}
			c.runtimeRefs++
			if key != "" {
				c.activeEnvRoots[key]++
			}
			c.mu.Unlock()
			c.aclMu.Unlock()
			return nil
		}
		c.mu.Unlock()
		c.aclMu.Unlock()
		<-removal
	}
}

func (c *sandboxStateCoordinator) canPruneACLs() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.resetting && c.runtimeRefs <= 1 && c.activeUses <= 1
}

func (c *sandboxStateCoordinator) unregisterRuntime(envRoot string) {
	if c == nil {
		return
	}
	c.aclMu.Lock()
	defer c.aclMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtimeRefs > 0 {
		c.runtimeRefs--
	}
	if key := pathutil.Key(envRoot); key != "" {
		if refs := c.activeEnvRoots[key]; refs > 1 {
			c.activeEnvRoots[key] = refs - 1
		} else {
			delete(c.activeEnvRoots, key)
		}
	}
}

func (c *sandboxStateCoordinator) beginUse(envRoot string) (func(), error) {
	if c == nil {
		return func() {}, nil
	}
	c.aclMu.Lock()
	defer c.aclMu.Unlock()
	c.mu.Lock()
	if c.resetting {
		c.mu.Unlock()
		return nil, errSandboxStateBusy
	}
	c.activeUses++
	envKey := pathutil.Key(envRoot)
	if envKey != "" {
		if c.activeEnvRoots == nil {
			c.activeEnvRoots = map[string]int{}
		}
		c.activeEnvRoots[envKey]++
	}
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.aclMu.Lock()
			defer c.aclMu.Unlock()
			c.mu.Lock()
			if c.activeUses > 0 {
				c.activeUses--
			}
			if envKey != "" {
				if refs := c.activeEnvRoots[envKey]; refs > 1 {
					c.activeEnvRoots[envKey] = refs - 1
				} else {
					delete(c.activeEnvRoots, envKey)
				}
			}
			c.mu.Unlock()
		})
	}, nil
}

func (c *sandboxStateCoordinator) beginReset() (func(), error) {
	if c == nil {
		return func() {}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resetting || c.activeUses > 0 || c.runtimeRefs > 1 {
		return nil, errSandboxStateBusy
	}
	c.resetting = true
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.resetting = false
			c.mu.Unlock()
		})
	}, nil
}

func (c *sandboxStateCoordinator) protectsEnvRoot(path string) bool {
	if c == nil {
		return false
	}
	key := pathutil.Key(path)
	if key == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeEnvRoots[key] > 0
}

func (c *sandboxStateCoordinator) withUnusedEnvRoot(path string, remove func() error) (bool, error) {
	if c == nil {
		return true, remove()
	}
	key := pathutil.Key(path)
	if key == "" {
		return false, nil
	}
	c.mu.Lock()
	if c.envRemovals == nil {
		c.envRemovals = map[string]chan struct{}{}
	}
	if c.activeEnvRoots[key] > 0 || c.envRemovals[key] != nil {
		c.mu.Unlock()
		return false, nil
	}
	done := make(chan struct{})
	c.envRemovals[key] = done
	c.mu.Unlock()

	var err error
	func() {
		defer func() {
			c.mu.Lock()
			delete(c.envRemovals, key)
			close(done)
			c.mu.Unlock()
		}()
		err = remove()
	}()
	return true, err
}
