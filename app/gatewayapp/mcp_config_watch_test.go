package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func TestMCPWatcherRetriesUnchangedFilesAndSeesSameTimestampReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := &stack.composition
	doc, err := s.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.WorkspaceTrust, err = workspacetrust.Set(doc.WorkspaceTrust, s.workspace.CWD, workspacetrust.Trusted)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.workspace.CWD, ".mcp.json")
	stamp := time.Unix(1000, 0)
	write := func(file, name string) {
		t.Helper()
		data := fmt.Sprintf(`{"mcpServers":{"%s":{"command":%q}}}`, name, os.Args[0])
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	write(path, "one")
	attempts := make(chan []mcp.ServerSpec, 8)
	first := true
	stop := s.watchConfiguredMCP(doc, func(specs []mcp.ServerSpec) error {
		attempts <- specs
		if first {
			first = false
			return errors.New("temporary reconciliation failure")
		}
		return nil
	})
	defer stop()
	waitFor := func(name string) {
		t.Helper()
		for {
			select {
			case got := <-attempts:
				if slices.ContainsFunc(got, func(spec mcp.ServerSpec) bool { return spec.Name == name }) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("watcher failed to apply %s: %v", name, ctx.Err())
			}
		}
	}
	waitFor("one")
	waitFor("one")
	write(path+".new", "two")
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	waitFor("two")
}
