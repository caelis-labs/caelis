//go:build darwin || linux || windows

package gatewayapp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func TestApplicationExecutionEnvironmentAfterHostApproval(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native application execution")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	cwd, home := filepath.Join(root, "work"), filepath.Join(root, "user-home")
	for _, dir := range []string{cwd, home} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	provider := &nativeModelScript{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), cwd, provider)
	defer host.close(t)
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "connect", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"}})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := registerApplicationHTTP(t, ctx, host, "environment", filepath.Join(root, "application.credential"))
	inherit := false
	environment := map[string]string{"HOME": home, "PATH": "/usr/bin:/bin", "CONFIG_VALUE": "configured"}
	command := `printf '%s|%s|%s' "$HOME" "$PWD" "$CONFIG_VALUE" > approved-env`
	if runtime.GOOS == "windows" {
		environment["SystemRoot"] = os.Getenv("SystemRoot")
		environment["TEMP"], environment["TMP"] = cwd, cwd
		delete(environment, "PATH")
		command = "[IO.File]::WriteAllText('approved-env', ($env:HOME+'|'+(Get-Location).Path+'|'+$env:CONFIG_VALUE))"
	}
	profile := application.Profile{Version: "v1", Model: "openai-compatible/gpt-4.1", ToolsVersion: "v1", Execution: "workspace-write", Workspace: application.Workspace{CWD: cwd}, ExecutionConfig: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Inherit: &inherit, Set: environment}}}
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	input, err := json.Marshal(map[string]any{"command": command, "sandbox_permissions": "require_escalated", "justification": "Verify the synthetic Host-route environment after explicit approval."})
	if err != nil {
		t.Fatal(err)
	}
	provider.set(nativeModelTool{"RunCommand", string(input)})
	_, err = client.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "prompt", SessionID: created.SessionID}, Input: "Run the synthetic environment check after approval."}, SourceKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var approval *appserver.ActiveApproval
	for approval == nil {
		select {
		case delivery, ok := <-feed.Subscription.Deliveries():
			if !ok {
				t.Fatal("approval feed closed before permission request")
			}
			for _, event := range delivery.Events {
				if event.Kind == eventstream.KindRequestPermission {
					state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
					if err != nil {
						t.Fatal(err)
					}
					approval = state.Approval.Active
				}
				if eventstream.IsTurnTerminalLifecycle(event) {
					t.Fatal("command completed without obtaining Host approval")
				}
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	output := filepath.Join(cwd, "approved-env")
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("command had effects before approval: %v", err)
	}
	option := ""
	for _, candidate := range approval.Permission.Options {
		if strings.HasPrefix(candidate.Kind, "allow_once") {
			option = candidate.ID
			break
		}
	}
	if option == "" {
		t.Fatal("Host request lacks a one-shot approval option")
	}
	_, err = client.ResolveApproval(ctx, appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{OperationID: "approve", SessionID: created.SessionID}, Target: approval.Target, ApprovalRequestID: string(approval.RequestID), Outcome: "selected", OptionID: option, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, created.SessionID)
	clients, err := httpclient.AppServerClients(client)
	if err != nil {
		t.Fatal(err)
	}
	var settled taskstream.TaskDescriptor
	for {
		listed, err := clients.Tasks.List(ctx, taskstream.ListRequest{SessionID: created.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		if len(listed.Tasks) == 1 && !listed.Tasks[0].Running {
			settled = listed.Tasks[0]
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("approved command did not settle: %+v, %v", listed, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if settled.State != "completed" {
		t.Fatalf("approved command failed: %+v", settled)
	}
	resolvedCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != home+"|"+resolvedCWD+"|configured" {
		t.Fatalf("approved command lost the pinned environment: %q, %v", got, err)
	}
}
