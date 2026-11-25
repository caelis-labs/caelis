package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ConnectionPool owns MCP startup, directory refresh and transport lifetime for
// independent Managers in one Host. Each Manager still owns its selection,
// projected namespaces and call admission.
type ConnectionPool struct {
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	start          clientStarter
	servers        map[[sha256.Size]byte]*managedServer
	workers        sync.WaitGroup
	refreshWorkers sync.WaitGroup
	closed         bool
	closeOnce      sync.Once
}

// NewConnectionPool creates a lazy pool. The owner must close it after its
// Managers stop admitting work.
func NewConnectionPool(ctx context.Context) *ConnectionPool {
	ctx, cancel := context.WithCancel(ctx)
	return &ConnectionPool{ctx: ctx, cancel: cancel, servers: make(map[[sha256.Size]byte]*managedServer)}
}

// NewManager attaches an ordered directory to the pool and starts only
// connections that are missing. The caller releases its directory with Close.
func (p *ConnectionPool) NewManager(specs []ServerSpec, onFailure func(ServerFailure)) (*Manager, error) {
	m := &Manager{pool: p, mu: &p.mu, initialized: make(chan struct{}), onFailure: onFailure}
	p.mu.Lock()
	if err := p.reconcileLocked(map[*Manager][]ServerSpec{m: specs}); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	initial := slices.Clone(m.servers)
	p.mu.Unlock()
	go func() {
		for _, server := range initial {
			<-server.initialized
		}
		close(m.initialized)
	}()
	return m, nil
}

// Reconcile publishes all supplied desired directories atomically. A connection
// used by another configuration stays live; a superseded connection stops
// accepting new calls as soon as the new directories are published.
func (p *ConnectionPool) Reconcile(desired map[*Manager][]ServerSpec) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reconcileLocked(desired)
}

func (p *ConnectionPool) reconcileLocked(desired map[*Manager][]ServerSpec) error {
	if p.closed || p.ctx.Err() != nil {
		return errors.New("MCP connection pool is closed")
	}
	for manager, specs := range desired {
		if manager.pool != p || manager.closed {
			return errors.New("MCP manager is closed or belongs to a different pool")
		}
		seen := make(map[string]bool, len(specs))
		for _, spec := range specs {
			if err := ValidateServerIdentity(spec); err != nil {
				return err
			}
			key := spec.PluginID + "/" + spec.Name
			if seen[key] {
				return fmt.Errorf("mcp manager: duplicate server %s", key)
			}
			seen[key] = true
		}
	}
	// Attach every desired connection before retiring any unused one. Moving
	// a connection between selections in a single update preserves its client.
	var detached []*managedServer
	for manager, specs := range desired {
		detached = manager.reconcileLocked(specs, detached)
	}
	for _, server := range detached {
		if len(server.members) == 0 {
			p.retireLocked(server)
		}
	}
	return nil
}

func (m *Manager) reconcileLocked(specs []ServerSpec, detached []*managedServer) []*managedServer {
	next := make([]*managedServer, 0, len(specs))
	nextSpecs := make(map[*managedServer]ServerSpec, len(specs))
	retainedNames := make(map[string]bool, len(specs))
	for _, spec := range specs {
		spec.Args = append([]string(nil), spec.Args...)
		spec.ReplaySourceIDs = append([]string(nil), spec.ReplaySourceIDs...)
		spec.PrefixTools = append([]string(nil), spec.PrefixTools...)
		spec.Env = maps.Clone(spec.Env)
		spec.Headers = maps.Clone(spec.Headers)
		server := m.pool.attachLocked(m, spec)
		next = append(next, server)
		nextSpecs[server] = spec
		retainedNames[spec.PluginID+"/"+spec.Name] = true
	}
	for _, server := range m.servers {
		if _, retained := nextSpecs[server]; retained {
			continue
		}
		delete(server.members, m)
		delete(m.revoked, server)
		detached = append(detached, server)
		key := server.spec.PluginID + "/" + server.spec.Name
		if !retainedNames[key] {
			for name, owner := range m.owners {
				if strings.HasPrefix(owner, key+"/") {
					delete(m.owners, name)
				}
			}
		}
	}
	if !slices.Equal(m.servers, next) || !reflect.DeepEqual(m.specs, nextSpecs) {
		m.servers, m.specs = next, nextSpecs
		m.rebuildToolsLocked()
	}
	return detached
}

func (p *ConnectionPool) attachLocked(manager *Manager, spec ServerSpec) *managedServer {
	launch := prepareLaunch(spec, nil)
	key := connectionKey(spec, launch)
	if server := p.servers[key]; server != nil {
		if !server.members[manager] {
			server.members[manager] = true
			if server.err != nil {
				p.notifyLocked(manager, server, server.err)
			}
		}
		return server
	}
	ctx, cancel := context.WithCancel(p.ctx)
	server := &managedServer{
		key: key, members: map[*Manager]bool{manager: true}, initialized: make(chan struct{}),
		spec: spec, cancel: cancel, refreshGate: make(chan struct{}, 1),
	}
	p.servers[key] = server
	start := p.start
	if start == nil {
		start = launch.start
	}
	p.workers.Go(func() {
		defer close(server.initialized)
		p.initialize(ctx, server, start, p.refreshCatalog)
	})
	return server
}

func connectionKey(spec ServerSpec, launch preparedLaunch) [sha256.Size]byte {
	dataDirRoot := spec.DataDirRoot
	// Invalid launches keep their original spec so independent failures stay
	// independent. For a valid launch compare the exact values sent to exec
	// or the HTTP transport, plus provenance and any private data root.
	if launch.err == nil {
		spec.Transport = launch.name
		spec.Command, spec.URL = "", strings.TrimSpace(spec.URL)
		spec.CleanEnvironment, spec.Env = false, nil
		spec.Args, spec.WorkDir = nil, ""
		switch transport := launch.transport.(type) {
		case *mcpsdk.CommandTransport:
			spec.Args, spec.WorkDir = transport.Command.Args, transport.Command.Dir
			spec.URL, spec.Headers = "", nil
			dataDirRoot = ""
			if spec.DataDir != "" {
				dataDirRoot = spec.effectiveDataDirRoot()
			}
		default:
			spec.DataDir, dataDirRoot = "", ""
			if len(spec.Headers) == 0 {
				spec.Headers = nil
			}
		}
	}
	spec.ReplaySourceIDs = nil
	raw, _ := json.Marshal(struct {
		Server         ServerSpec
		Scope          string
		LaunchIdentity string
		DataDirRoot    string `json:",omitempty"`
	}{spec, spec.ConnectionScope, launch.identity, dataDirRoot})
	return sha256.Sum256(raw)
}

func (p *ConnectionPool) notifyLocked(manager *Manager, server *managedServer, err error) {
	if manager.onFailure == nil {
		return
	}
	failure := ServerFailure{PluginID: server.spec.PluginID, Name: server.spec.Name, Err: err}
	manager.notices.Add(1)
	p.workers.Go(func() {
		defer manager.notices.Done()
		manager.onFailure(failure)
	})
}

func (p *ConnectionPool) rebuildDirectoriesLocked(server *managedServer) {
	for member := range server.members {
		member.rebuildToolsLocked()
	}
}

// Close stops new admission, cancels startup and joins transport cleanup.
func (p *ConnectionPool) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.cancel()
		for _, server := range p.servers {
			p.retireLocked(server)
			p.rebuildDirectoriesLocked(server)
		}
		p.mu.Unlock()
		p.workers.Wait()
		p.refreshWorkers.Wait()
	})
	return nil
}

// retireLocked stops admission before waiting for calls which already borrowed
// the client. Without an installed client, initialization owns its cleanup.
func (p *ConnectionPool) retireLocked(server *managedServer) {
	if server.retired {
		return
	}
	delete(p.servers, server.key)
	server.retired = true
	server.cancel()
	client := server.client
	if client == nil {
		return
	}
	var drained <-chan struct{}
	if server.inFlight != 0 {
		server.drained = make(chan struct{})
		drained = server.drained
	}
	p.workers.Go(func() {
		if drained != nil {
			select {
			case <-drained:
			case <-p.ctx.Done():
			}
		}
		_ = client.Close()
	})
}

func (p *ConnectionPool) initialize(parent context.Context, server *managedServer, start clientStarter, refresh func(context.Context, *managedServer, *Manager) error) {
	ctx, cancel := context.WithTimeout(parent, DefaultStartupTimeout)
	client, err := start(ctx, server.spec)
	if err == nil {
		err = ctx.Err()
	}
	cancel()
	if err != nil {
		var phase *startupError
		if !errors.As(err, &phase) {
			err = &startupError{Stage: "connection", Err: err}
		}
	}
	p.mu.Lock()
	if p.closed || server.retired || parent.Err() != nil {
		p.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		return
	}
	server.err = err
	if err != nil {
		p.notifyMembersLocked(server, err)
	}
	if err == nil {
		server.client = client
	}
	p.rebuildDirectoriesLocked(server)
	p.mu.Unlock()
	if err == nil {
		refreshErr := refresh(parent, server, nil)
		if refreshErr != nil {
			refreshErr = client.startupFailure("tools/list", refreshErr)
		}
		p.mu.Lock()
		if p.closed || server.retired {
			// Retirement or Manager.Close owns the installed client.
			p.mu.Unlock()
			return
		}
		if parent.Err() != nil {
			p.mu.Unlock()
			_ = client.Close()
			return
		}
		server.err = refreshErr
		if refreshErr != nil {
			p.notifyMembersLocked(server, refreshErr)
		}
		server.catalogReady = true
		p.rebuildDirectoriesLocked(server)
		p.mu.Unlock()
		p.refreshWorkers.Go(func() { p.watchCatalog(parent, server) })
	}
	if err != nil && client != nil {
		_ = client.Close()
	}
}

func (p *ConnectionPool) notifyMembersLocked(server *managedServer, err error) {
	for member := range server.members {
		p.notifyLocked(member, server, err)
	}
}
