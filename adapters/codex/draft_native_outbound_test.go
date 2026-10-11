package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	outbound "github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

// Both production endpoints must reject a controlled-profile migration when
// the real native backend cannot provide atomic idle-only admission. This is
// an explicit capability failure, not a reason to fall back or submit work.
func TestDraftInstalledNativeCaelisRejectsIncompleteConditionalProfile(t *testing.T) {
	if os.Getenv("CAELIS_TEST_NATIVE_CODEX") == "" {
		t.Skip("set CAELIS_TEST_NATIVE_CODEX to a Codex executable to test native interoperability")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "no model work may be submitted", http.StatusInternalServerError)
	}))
	defer provider.Close()
	cwd := t.TempDir()
	backend, _, _ := newInstalledDraftBackend(t, ctx, cwd, provider.URL)
	peerInput, peerOutput := io.Pipe()
	serverInput, serverOutput := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- backend.ServeACPDraft(ctx, ConnectionOptions{Workspace: WorkspacePolicy{AllowedRoots: []string{cwd}, WritableRoots: []string{cwd}}}, acpdraft.SchemaTag, serverInput, peerOutput)
	}()
	client, err := outbound.NewStreamClient(serverOutput, peerInput, outbound.Config{ACPDraftSchema: acpdraft.SchemaTag})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close(context.Background())
		_ = peerInput.Close()
		_ = peerOutput.Close()
		_ = serverInput.Close()
		_ = serverOutput.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("controlled draft connection did not close")
		}
	})
	if _, err := client.Initialize(ctx); err == nil || !strings.Contains(err.Error(), "idle-only") {
		t.Fatalf("production client admitted an incomplete conditional profile: %v", err)
	}
	backend.mu.Lock()
	owners := len(backend.routes)
	backend.mu.Unlock()
	if owners != 0 || calls.Load() != 0 {
		t.Fatalf("failed migration submitted work: owners=%d modelCalls=%d", owners, calls.Load())
	}
	t.Log("real native idleOnly:false was rejected by the production controlled client before Session creation or model work; no fallback")
}
