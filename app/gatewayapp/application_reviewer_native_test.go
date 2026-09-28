//go:build darwin || linux

package gatewayapp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// This check uses the actual native Application executor and controlled provider
// transports. An outer sandbox may prevent native sandbox setup.
func TestApplicationReviewerNativeApprovalContinuation(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native Application approval acceptance")
	}
	for _, name := range []string{"Write", "RunCommand"} {
		for _, allow := range []bool{true, false} {
			label := "deny"
			if allow {
				label = "allow"
			}
			t.Run(name+"/"+label, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				root := t.TempDir()
				workspace := filepath.Join(root, "workspace")
				if err := os.Mkdir(workspace, 0700); err != nil {
					t.Fatal(err)
				}
				// System temp is an ordinary writable root. Use isolated local
				// scratch to exercise an actual outside-root file approval.
				effectDir, err := os.MkdirTemp(".", ".application-review-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.RemoveAll(effectDir); err != nil {
						t.Error(err)
					}
				})
				target, err := filepath.Abs(filepath.Join(effectDir, "effect.txt"))
				if err != nil {
					t.Fatal(err)
				}
				args := map[string]any{"path": target, "content": "NATIVE_ONCE\n"}
				if name == "RunCommand" {
					args = map[string]any{"command": "printf 'NATIVE_ONCE\\n' >> " + "'" + strings.ReplaceAll(target, "'", "'\\''") + "'",
						"sandbox_permissions": "require_escalated", "justification": "Write the requested synthetic fixture outside the workspace", "yield_time_ms": 1000}
				}
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				decision := `{"option_id":"reject_once","rationale":"The requested synthetic operation is not authorized."}`
				if allow {
					decision = `{"option_id":"allow_once"}`
				}
				provider := &reviewerHTTPProvider{toolName: name, args: string(raw), decision: decision}
				host, client, _ := setupReviewerHTTP(t, ctx, root, provider)
				defer host.close(t)
				profile := application.Profile{Version: "native-review/1", Model: "openai-compatible/gpt-4.1", ToolsVersion: "native/1",
					Execution: "workspace-write", Workspace: application.Workspace{CWD: workspace}, NativeTools: []string{name, "Task"},
					Permissions: application.Permissions{Mode: "workspace-write", ApprovalMode: "auto-review"},
					Reviewer:    &application.Reviewer{Kind: "guardian", Model: "openai-compatible/reviewer-model"}}
				created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "native-review"}, Profile: profile})
				if err != nil || created.SessionID == "" {
					t.Fatal(created, err)
				}
				result, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{
					PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: "native-effect"}, Input: "Write NATIVE_ONCE to the exact fixture path with the selected operation."}, SourceKind: "user"})
				if err != nil {
					diagnostics, _ := os.ReadFile(filepath.Join(root, "store", "logs", "runtime.jsonl"))
					t.Fatalf("prompt: %+v, %v; diagnostics=%s", result, err, diagnostics)
				}
				waitApplicationHTTPIdle(t, ctx, client, created.SessionID)
				data, readErr := os.ReadFile(target)
				if allow && (readErr != nil || string(data) != "NATIVE_ONCE\n") {
					t.Fatalf("native approval did not resume exact action once: %q, %v", data, readErr)
				}
				if !allow && !os.IsNotExist(readErr) {
					t.Fatalf("denied native action executed: %q, %v", data, readErr)
				}
				wantStatus := "denied"
				if allow {
					wantStatus = "approved"
				}
				found := false
				for _, event := range applicationHTTPHistory(t, ctx, client, created.SessionID) {
					if event.Kind == eventstream.KindApprovalReview && event.ApprovalReview != nil && event.ApprovalReview.Status == wantStatus && event.TurnID == result.Target.TurnID && event.ApprovalReview.ToolName == name {
						found = true
					}
				}
				if !found {
					t.Fatal("missing native review outcome")
				}
			})
		}
	}
}
