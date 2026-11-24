package mcp

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestConnectionPoolUsesEffectiveLaunch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CAELIS_MCP_POOL_KEY", "effective-value")
	stdio := ServerSpec{
		PluginID: "test", Name: "service", Command: os.Args[0], WorkDir: root,
		Env: map[string]string{"CAELIS_MCP_POOL_KEY": "effective-value"},
	}
	key := func(spec ServerSpec) [32]byte {
		t.Helper()
		launch := prepareLaunch(spec, nil)
		if launch.err != nil {
			t.Fatal(launch.err)
		}
		return connectionKey(spec, launch)
	}
	equivalent := stdio
	equivalent.Transport, equivalent.Env, equivalent.PrefixTools = "command", nil, []string{"echo"}
	if key(stdio) != key(equivalent) {
		t.Fatal("equivalent effective launches declined to share")
	}
	for _, tc := range []struct {
		name   string
		change func(*ServerSpec)
	}{
		{"working directory", func(s *ServerSpec) { s.WorkDir = t.TempDir() }},
		{"data directory", func(s *ServerSpec) { s.DataDir = t.TempDir() }},
		{"arguments", func(s *ServerSpec) { s.Args = []string{"different"} }},
		{"environment", func(s *ServerSpec) { s.Env = map[string]string{"CAELIS_MCP_POOL_KEY": "different"} }},
		{"authority", func(s *ServerSpec) { s.ConnectionScope = "private" }},
		{"package version", func(s *ServerSpec) { s.PackageVersion = "2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := stdio
			tc.change(&changed)
			if key(stdio) == key(changed) {
				t.Fatal("different effective launches shared a key")
			}
		})
	}
	http := ServerSpec{PluginID: "test", Name: "service", URL: "https://fixture.invalid/mcp", Headers: map[string]string{"Authorization": "Bearer fixture-a"}}
	other := http
	other.WorkDir = t.TempDir()
	if key(http) != key(other) {
		t.Fatal("an unused working directory partitioned the same HTTP endpoint")
	}
	other.Headers = map[string]string{"Authorization": "Bearer fixture-b"}
	if key(http) == key(other) {
		t.Fatal("HTTP credentials shared a key")
	}
}

func TestConnectionPoolSharesColdStartupAndKeepsAdmissionLocal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewConnectionPool(t.Context())
		defer pool.Close()
		gate := make(chan struct{})
		var starts atomic.Int32
		pool.start = func(ctx context.Context, spec ServerSpec) (*Client, error) {
			starts.Add(1)
			return asyncTestClient(ctx, spec, gate)
		}
		spec := ServerSpec{PluginID: "test", Name: "shared"}
		var managers [2]*Manager
		var workers sync.WaitGroup
		for i := range managers {
			workers.Go(func() {
				view := spec
				if i == 0 {
					view.PrefixTools = []string{"echo"}
				}
				manager, err := pool.NewManager([]ServerSpec{view}, nil)
				if err != nil {
					t.Error(err)
				}
				managers[i] = manager
			})
		}
		workers.Wait()
		if t.Failed() {
			t.FailNow()
		}
		for _, manager := range managers {
			defer manager.Close()
		}
		synctest.Wait()
		if got := starts.Load(); got != 1 {
			t.Fatalf("concurrent clients started %d times, want 1", got)
		}
		close(gate)
		for _, manager := range managers {
			<-manager.Initialized()
		}
		native, deferred := managers[0], managers[1]
		if len(native.PrefixTools()) != 1 || len(deferred.PrefixTools()) != 0 || len(deferred.SearchTools()) != 1 {
			t.Fatal("declaration settings leaked across the shared transport")
		}
		old := native.Tools()[0]
		if err := native.RevokeServer(spec.PluginID, spec.Name); err != nil {
			t.Fatal(err)
		}
		if _, err := old.Call(t.Context(), tool.Call{Input: []byte(`{}`)}); errorcode.CodeOf(err) != errorcode.PermissionDenied {
			t.Fatalf("revoked Manager's retained tool = %v", err)
		}
		if err := native.Close(); err != nil {
			t.Fatal(err)
		}
		if tools := deferred.SearchTools(); len(tools) != 1 {
			t.Fatal("revoking and closing one Manager removed another Manager's tool")
		}
	})
}
