package gatewayapp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/memorytool"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestStoreBackupRestoreRebuildsAcceptedModelContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := newMemoryGoldenProvider(t)
	stack := newMemoryGoldenStack(t, provider, storeDir, workspace)
	provider.Begin(memoryGoldenScenario{name: "backup-source", actions: []memoryGoldenAction{
		{name: memorytool.RememberToolName, arguments: `{"text":"the accepted backup fact is durable"}`},
	}})
	active := runMemoryGoldenSession(t, ctx, stack, "backup-session")
	provider.AssertComplete(t)

	provider.Begin(memoryGoldenScenario{name: "context-before-backup"})
	if _, err := runHeadlessOnceForGatewayAppTest(ctx, stack, active, active.SessionID, "capture context before backup", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	provider.AssertComplete(t)
	beforeMessages := provider.LastMessages(t)

	var archive bytes.Buffer
	if _, err := stack.WriteStoreBackup(ctx, &archive); err != nil {
		t.Fatalf("Stack.WriteStoreBackup() error = %v", err)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreStoreWithEmbeddedMemory(ctx, storeDir, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatalf("RestoreStoreWithEmbeddedMemory() error = %v", err)
	}
	// The spool is disposable and must not become the source of replay after
	// the restore. Canonical Session JSONL remains available to reconstruction.
	if err := os.RemoveAll(controlStreamSpoolRoot(storeDir)); err != nil {
		t.Fatal(err)
	}
	reopened := newMemoryGoldenStack(t, provider, storeDir, workspace)
	defer reopened.Close()
	provider.Begin(memoryGoldenScenario{name: "context-after-backup"})
	if _, err := runHeadlessOnceForGatewayAppTest(ctx, reopened, active, active.SessionID, "capture context after backup", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	provider.AssertComplete(t)
	afterMessages := provider.LastMessages(t)
	wantMessages := append(cloneMemoryGoldenMessages(beforeMessages),
		map[string]any{"role": "assistant", "content": "memory-golden-ok"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "capture context after backup"},
		}},
	)
	if !reflect.DeepEqual(afterMessages, wantMessages) {
		wantJSON, _ := json.Marshal(wantMessages)
		gotJSON, _ := json.Marshal(afterMessages)
		t.Fatalf("restored model context changed:\nwant=%s\ngot=%s", wantJSON, gotJSON)
	}
}
