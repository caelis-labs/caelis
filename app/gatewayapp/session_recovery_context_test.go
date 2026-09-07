package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/memoryhost"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/memorytool"
	"github.com/caelis-labs/caelis/surfaces/headless"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

func TestSessionRecoveryNextModelRequestUsesCanonicalToolResultsWithoutSpool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := newMemoryGoldenProvider(t)
	stack := newMemoryGoldenStack(t, provider, storeDir, workspace)
	t.Cleanup(func() { _ = stack.Close() })
	provider.Begin(memoryGoldenScenario{name: "canonical-tools", actions: []memoryGoldenAction{
		{name: memorytool.RememberToolName, arguments: `{"text":"` + memoryGoldenFact + `"}`},
		{name: memorytool.RecallToolName, arguments: `{"query":"preferred review language"}`},
	}})
	active := runMemoryGoldenSession(t, ctx, stack, "canonical-recovery")
	provider.AssertComplete(t)
	before := provider.LastMessages(t)
	beforeResults := memoryGoldenToolResults(t, stack, active.SessionRef)
	assertMemoryGoldenResult(t, beforeResults, memorytool.RecallToolName, memoryGoldenFact)
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	// The disposable transport spool cannot be the source of reconstruction.
	if err := os.RemoveAll(controlStreamSpoolRoot(storeDir)); err != nil {
		t.Fatal(err)
	}
	guard := &recoveryMemoryGuard{}
	var err error
	stack, err = newGatewayAppTestStack(t, Config{
		AppName: "caelis-memory", UserID: "memory-golden", StoreDir: storeDir,
		WorkspaceKey: "memory-golden", WorkspaceCWD: workspace, SkillDirs: []string{},
		Sandbox: SandboxConfig{RequestedType: "host"}, memoryHost: guard,
		ResolveProviderHTTPClient: func(context.Context, ModelConfig) (*http.Client, error) { return provider.Client(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.Begin(memoryGoldenScenario{name: "canonical-reopened"})
	if _, err := runHeadlessOnceForGatewayAppTest(ctx, stack, active, active.SessionID, "continue from canonical history", headless.Options{}); err != nil {
		t.Fatal(err)
	}
	provider.AssertComplete(t)
	want := append(cloneMemoryGoldenMessages(before),
		map[string]any{"role": "assistant", "content": "memory-golden-ok"},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "continue from canonical history"}}},
	)
	got := provider.LastMessages(t)
	if !reflect.DeepEqual(got, want) {
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		t.Fatalf("next model context changed:\nwant=%s\ngot=%s", wantJSON, gotJSON)
	}
	if calls := guard.calls.Load(); calls != 0 {
		t.Fatalf("replay made %d Memory calls", calls)
	}
	if after := memoryGoldenToolResults(t, stack, active.SessionRef); !reflect.DeepEqual(after, beforeResults) {
		t.Fatalf("replay changed canonical tool results: %#v != %#v", after, beforeResults)
	}
}

type recoveryMemoryGuard struct {
	runtimeMemoryHostStub
	calls atomic.Int32
}

func (g *recoveryMemoryGuard) Bind(memorybinding.RuntimeMemoryBindingSnapshot, v1alpha1.SourceContext, v1alpha1.RecallBudget) (memoryhost.BoundClient, error) {
	return g, nil
}
func (g *recoveryMemoryGuard) Remember(context.Context, string, string, *time.Time) (v1alpha1.RememberResponse, error) {
	g.calls.Add(1)
	return v1alpha1.RememberResponse{}, errors.New("replay must not Remember")
}
func (g *recoveryMemoryGuard) Recall(context.Context, string, v1alpha1.ConsistencyToken) (v1alpha1.RecallResponse, error) {
	g.calls.Add(1)
	return v1alpha1.RecallResponse{}, errors.New("replay must not Recall")
}
func (g *recoveryMemoryGuard) GetReceiptStatus(context.Context, v1alpha1.ReceiptID) (v1alpha1.ReceiptStatus, error) {
	g.calls.Add(1)
	return v1alpha1.ReceiptStatus{}, errors.New("replay must not query Memory")
}
