//go:build darwin || linux || windows

package gatewayapp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/taskstream"
)

func TestSharedWorkerNativeHTTPCommandAndRecovery(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native Worker acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "worker 工作 目录")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("WORKER_ONCE"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &nativeModelScript{}
	host := startApplicationHTTPHostWithSandbox(t, filepath.Join(root, "store"), workspace, provider, "")
	defer func() { host.close(t) }()
	client, _ := nativeHostSession(t, ctx, host, workspace)
	create := appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "native-worker-create"}, CWD: workspace, Model: "openai-compatible/gpt-4.1"}
	created, err := client.CreateWorker(ctx, create)
	if err != nil || created.SessionID == "" || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("native Worker creation = %+v, %v", created, err)
	}
	sid := created.SessionID
	command := nativeCommandTool(t, nativeCopyCommand([]string{"input.txt"}, "result.txt"))
	var args map[string]any
	if err := json.Unmarshal([]byte(command.Input), &args); err != nil {
		t.Fatal(err)
	}
	args["yield_time_ms"] = 0
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	provider.set(nativeModelTool{"RunCommand", string(raw)}, nativeModelTool{"Task", `{"action":"wait","handle":"command"}`})
	prompt := appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: sid, OperationID: "native-worker-prompt"}, Input: "Copy input.txt to result.txt once using the native command."}
	started, err := client.Prompt(ctx, prompt)
	if err != nil || started.Target.TurnID == "" {
		t.Fatalf("Worker prompt = %+v, %v", started, err)
	}
	waitApplicationHTTPIdle(t, ctx, client, sid)
	clients, err := httpclient.AppServerClients(client)
	if err != nil {
		t.Fatal(err)
	}
	var settled taskstream.TaskDescriptor
	for {
		listed, err := clients.Tasks.List(ctx, taskstream.ListRequest{SessionID: sid})
		if err != nil {
			t.Fatal(err)
		}
		if len(listed.Tasks) == 1 && !listed.Tasks[0].Running {
			settled = listed.Tasks[0]
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Worker native Task did not settle: %+v", listed)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if settled.State != "completed" {
		t.Fatalf("native Task = %+v", settled)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "result.txt")); err != nil || string(data) != "WORKER_ONCE" {
		t.Fatalf("native Worker effect = %q, %v", data, err)
	}
	state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil || state.Run.Active || state.Run.Status != "completed" || state.Run.RunID == "" {
		t.Fatalf("native Worker state = %+v, %v", state, err)
	}
	before := applicationHTTPHistory(t, ctx, client, sid)
	toolResults := 0
	terminal := false
	for _, event := range before {
		if eventstream.IsTurnTerminalLifecycle(event) && event.TurnID == started.Target.TurnID && event.Lifecycle.State == "completed" {
			terminal = true
		}
		if update, ok := event.Update.(eventstream.ToolCallUpdate); ok && update.Status != nil && *update.Status == "completed" {
			toolResults++
		}
	}
	if toolResults == 0 || !terminal {
		t.Fatal("native Worker lacks a tool result or the exact completed Turn")
	}
	provider.mu.Lock()
	requests := len(provider.seen)
	lastRequest := append([]byte(nil), provider.seen[requests-1]...)
	provider.mu.Unlock()
	host.close(t)
	host = startApplicationHTTPHostWithSandbox(t, filepath.Join(root, "store"), workspace, provider, "")
	secret, err := os.ReadFile(filepath.Join(root, "native.credential"))
	if err != nil {
		t.Fatal(err)
	}
	client = host.app(string(secret))
	for _, operation := range []string{create.OperationID, prompt.OperationID} {
		receipt, err := client.ApplicationOperation(ctx, operation)
		if err != nil || receipt.Result == nil || receipt.Result.SessionID != sid {
			t.Fatalf("recovered %s = %+v, %v", operation, receipt, err)
		}
	}
	if retry, err := client.Prompt(ctx, prompt); err != nil || !reflect.DeepEqual(retry, started) {
		t.Fatalf("original prompt retry = %+v, %v; want %+v", retry, err, started)
	}
	if retry, err := client.CreateWorker(ctx, create); err != nil || !reflect.DeepEqual(retry, created) {
		t.Fatalf("original Worker retry = %+v, %v; want %+v", retry, err, created)
	}
	provider.mu.Lock()
	if len(provider.seen) != requests {
		provider.mu.Unlock()
		t.Fatal("receipt recovery redispatched model or native work")
	}
	provider.mu.Unlock()
	afterIDs := applicationHTTPCanonicalIDs(applicationHTTPHistory(t, ctx, client, sid))
	for id := range applicationHTTPCanonicalIDs(before) {
		if !afterIDs[id] {
			t.Fatalf("Worker canonical event disappeared on restart: %s", id)
		}
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "result.txt")); err != nil || strings.TrimSpace(string(data)) != "WORKER_ONCE" {
		t.Fatalf("recovered native Worker effect = %q, %v", data, err)
	}
	provider.set(nativeModelTool{"Read", `{"path":"result.txt"}`})
	resumed, err := client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: sid, OperationID: "native-worker-resume"}, Input: "Read the existing result; preserve the completed command."})
	if err != nil || resumed.Target.TurnID == started.Target.TurnID {
		t.Fatalf("resumed Worker = %+v, %v", resumed, err)
	}
	waitApplicationHTTPIdle(t, ctx, client, sid)
	conversation := func(raw []byte) []map[string]any {
		t.Helper()
		var request struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		var messages []map[string]any
		for _, message := range request.Messages {
			if message["role"] != "system" {
				messages = append(messages, message)
			}
		}
		return messages
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	want := conversation(lastRequest)
	got := conversation(provider.seen[requests])
	if len(got) <= len(want) || !reflect.DeepEqual(got[:len(want)], want) {
		t.Fatal("Host restart did not rebuild the canonical model context")
	}
	if !strings.Contains(string(provider.seen[len(provider.seen)-1]), "WORKER_ONCE") {
		t.Fatal("resumed model did not receive the existing native file result")
	}
}
