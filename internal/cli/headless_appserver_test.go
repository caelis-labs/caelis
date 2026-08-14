package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/gatewayapptest"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestHeadlessExplicitEmbeddedRunBindsAppServer(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	clearSelfAgentEnv(t)
	t.Setenv("CAELIS_CONTROL_URL", "")
	t.Setenv("CAELIS_CONTROL_EMBEDDED", "")

	workspace := t.TempDir()
	t.Chdir(workspace)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runErr := runWithRoundTripEmbeddedControl(
		t,
		context.Background(),
		[]string{
			"--embedded",
			"-p", "exercise top-level Headless",
			"-format", "json",
			"-store-dir", cliTestStoreDir(t),
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if runErr == nil || !strings.Contains(runErr.Error(), "no model configured") {
		t.Fatalf("top-level run() error = %v, stdout=%q, stderr=%q", runErr, stdout.String(), stderr.String())
	}
	var record headlessErrorOutput
	if err := json.Unmarshal(stdout.Bytes(), &record); err != nil {
		t.Fatalf("decode top-level Headless output %q: %v", stdout.String(), err)
	}
	if record.SchemaVersion != headlessOutputSchemaVersion ||
		record.Type != headlessOutputTypeError ||
		record.SessionID == "" ||
		!strings.Contains(record.Message, "no model configured") {
		t.Fatalf("top-level Headless error = %#v", record)
	}
}

func TestHeadlessEmbeddedAppServerCoversCreateResumeAndFormats(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var callsMu sync.Mutex
	providerCalls := map[string]int{}
	provider := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			http.NotFound(w, request)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var prompt, answer string
		switch payload := string(body); {
		case strings.Contains(payload, "stream prompt"):
			prompt, answer = "stream prompt", "jsonl answer"
		case strings.Contains(payload, "resume prompt"):
			prompt, answer = "resume prompt", "json answer"
		case strings.Contains(payload, "first prompt"):
			prompt, answer = "first prompt", "plain answer"
		default:
			http.Error(w, "unexpected Headless model request", http.StatusBadRequest)
			return
		}
		callsMu.Lock()
		providerCalls[prompt]++
		callsMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(
			w,
			"data: {\"id\":\"headless-test\",\"object\":\"chat.completion.chunk\",\"model\":\"headless-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
			answer,
		)
	}))

	workspace := t.TempDir()
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:                   "caelis-headless-test",
		UserID:                    "headless-test",
		StoreDir:                  filepath.Join(t.TempDir(), "store"),
		WorkspaceKey:              "headless-workspace",
		WorkspaceCWD:              workspace,
		ApprovalMode:              "auto-review",
		SkillDirs:                 []string{},
		ResolveProviderHTTPClient: gatewayapptest.StaticProviderHTTPClient(provider.Client()),
		Sandbox:                   gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	if _, err := gatewayapptest.ConnectModel(context.Background(), host, gatewayapp.ModelConfig{
		Provider:   "openai-compatible",
		API:        providers.APIOpenAICompatible,
		Model:      "headless-test",
		BaseURL:    provider.URL,
		HTTPClient: provider.Client(),
		Token:      "headless-test-token",
		AuthType:   model.AuthBearerToken,
		Timeout:    2 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	server, err := local.NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	clients, tasks, err := server.Bind(controlclient.Principal{ID: host.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if err := clients.Validate(); err != nil {
		t.Fatal(err)
	}
	if tasks == nil {
		t.Fatal("embedded AppServer returned no Task client")
	}

	var plain bytes.Buffer
	createdSessionID, err := runHeadless(
		ctx,
		clients.Sessions,
		host.Workspace,
		"",
		"first prompt",
		outputText,
		&plain,
	)
	if err != nil {
		t.Fatal(err)
	}
	if createdSessionID == "" || plain.String() != "plain answer\n" {
		t.Fatalf("plain Headless result = Session %q, output %q", createdSessionID, plain.String())
	}

	var jsonOutput bytes.Buffer
	resumedSessionID, err := runHeadless(
		ctx,
		clients.Sessions,
		host.Workspace,
		createdSessionID,
		"resume prompt",
		outputJSON,
		&jsonOutput,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumedSessionID != createdSessionID {
		t.Fatalf("resumed Session = %q, want %q", resumedSessionID, createdSessionID)
	}
	var jsonResult runResult
	if err := json.Unmarshal(jsonOutput.Bytes(), &jsonResult); err != nil {
		t.Fatalf("decode JSON output %q: %v", jsonOutput.String(), err)
	}
	assertHeadlessResult(t, jsonResult, createdSessionID, "json answer")

	var jsonlOutput bytes.Buffer
	jsonlSessionID, err := runHeadless(
		ctx,
		clients.Sessions,
		host.Workspace,
		"",
		"stream prompt",
		outputJSONL,
		&jsonlOutput,
	)
	if err != nil {
		t.Fatal(err)
	}
	if jsonlSessionID == "" || jsonlSessionID == createdSessionID {
		t.Fatalf("fresh JSONL Session = %q, previous Session %q", jsonlSessionID, createdSessionID)
	}
	lines := strings.Split(strings.TrimSpace(jsonlOutput.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("JSONL output has %d records: %q", len(lines), jsonlOutput.String())
	}
	for index, line := range lines[:len(lines)-1] {
		var record headlessEnvelopeOutput
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode JSONL envelope %d: %v", index, err)
		}
		if record.SchemaVersion != headlessOutputSchemaVersion ||
			record.Type != headlessOutputTypeEnvelope ||
			record.SessionID != jsonlSessionID {
			t.Fatalf("JSONL envelope %d = %#v", index, record)
		}
	}
	var jsonlResult runResult
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &jsonlResult); err != nil {
		t.Fatalf("decode JSONL result: %v", err)
	}
	assertHeadlessResult(t, jsonlResult, jsonlSessionID, "jsonl answer")

	callsMu.Lock()
	defer callsMu.Unlock()
	for _, prompt := range []string{"first prompt", "resume prompt", "stream prompt"} {
		if providerCalls[prompt] == 0 {
			t.Fatalf("provider calls = %#v, want a model request for %q", providerCalls, prompt)
		}
	}
}

func assertHeadlessResult(t *testing.T, result runResult, sessionID, output string) {
	t.Helper()
	if result.SchemaVersion != headlessOutputSchemaVersion ||
		result.Type != headlessOutputTypeResult ||
		result.SessionID != sessionID ||
		result.Status != eventstream.LifecycleStateCompleted ||
		result.Output != output ||
		result.Turn.HandleID == "" ||
		result.Turn.RunID == "" ||
		result.Turn.TurnID == "" ||
		result.Cursor == "" {
		t.Fatalf("Headless result = %#v", result)
	}
}
