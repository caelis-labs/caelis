package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/internal/updater"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestRunServeStartsProductControlServer(t *testing.T) {
	t.Setenv("CAELIS_CONTROL_TOKEN", "0123456789abcdef0123456789abcdef0123456789abcdef")
	previous := runControlServerCommand
	t.Cleanup(func() { runControlServerCommand = previous })
	var captured controlserver.Config
	runControlServerCommand = func(_ context.Context, deps controlserver.Dependencies, config controlserver.Config) error {
		if deps.Services.Validate() != nil || deps.TaskStreams == nil || deps.Lifecycle == nil {
			t.Fatal("serve did not assemble the product Control and Task clients")
		}
		captured = config
		return nil
	}
	err := run(context.Background(), []string{
		"serve", "--store-dir", t.TempDir(), "--listen", "127.0.0.1:7777", "--sandbox-backend", "host",
	}, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Address != "127.0.0.1:7777" || captured.Authenticator == nil || captured.Principal.ID != "local-user" || captured.TokenFile != "" {
		t.Fatalf("control server config = %#v", captured)
	}
}

func TestRunServeRejectsBearerSecretInArgv(t *testing.T) {
	err := run(context.Background(), []string{
		"serve", "--control-token", "do-not-put-secrets-in-argv",
	}, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("run() error = %v, want removed --control-token flag", err)
	}
}

func TestRunServeDefaultsToPersistentTokenFile(t *testing.T) {
	t.Setenv("CAELIS_CONTROL_TOKEN", "")
	t.Setenv("CAELIS_CONTROL_TOKEN_FILE", "")
	previous := runControlServerCommand
	t.Cleanup(func() { runControlServerCommand = previous })
	var captured controlserver.Config
	runControlServerCommand = func(_ context.Context, _ controlserver.Dependencies, config controlserver.Config) error {
		captured = config
		return nil
	}
	storeDir := t.TempDir()
	err := run(context.Background(), []string{"serve", "--store-dir", storeDir, "--sandbox-backend", "host"}, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Authenticator != nil || captured.Principal.ID != "local-user" {
		t.Fatalf("control server config = %#v", captured)
	}
	if want := controlserver.DefaultTokenFile(storeDir); captured.TokenFile != want {
		t.Fatalf("TokenFile = %q, want %q", captured.TokenFile, want)
	}
}

func TestResolveInputFromPrompt(t *testing.T) {
	got, single, err := resolveInput("hello", strings.NewReader(""), true)
	if err != nil {
		t.Fatalf("resolveInput() error = %v", err)
	}
	if !single || got != "hello" {
		t.Fatalf("resolveInput() = %q, %v", got, single)
	}
}

func TestResolveTurnInputForceInteractiveDoesNotConsumePipe(t *testing.T) {
	stdin := strings.NewReader("piped prompt")
	got, single, err := resolveTurnInput("", stdin, false, true)
	if err != nil {
		t.Fatalf("resolveTurnInput() error = %v", err)
	}
	if single || got != "" {
		t.Fatalf("resolveTurnInput() = %q, %v", got, single)
	}

	remaining, err := io.ReadAll(stdin)
	if err != nil {
		t.Fatalf("ReadAll(stdin) error = %v", err)
	}
	if string(remaining) != "piped prompt" {
		t.Fatalf("remaining stdin = %q", remaining)
	}
}

func TestReaderIsTTYUsesInjectedReader(t *testing.T) {
	if readerIsTTY(strings.NewReader("prompt")) {
		t.Fatal("readerIsTTY(strings.Reader) = true, want false for injected non-file stdin")
	}
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer file.Close()
	if readerIsTTY(file) {
		t.Fatal("readerIsTTY(temp file) = true, want false for regular file")
	}
}

func TestParseOutputFormat(t *testing.T) {
	if got, err := parseOutputFormat("json"); err != nil || got != outputJSON {
		t.Fatalf("parseOutputFormat() = %q, %v", got, err)
	}
	if got, err := parseOutputFormat("jsonl"); err != nil || got != outputJSONL {
		t.Fatalf("parseOutputFormat(jsonl) = %q, %v", got, err)
	}
	if _, err := parseOutputFormat("xml"); err == nil {
		t.Fatal("parseOutputFormat(xml) error = nil")
	}
}

func TestHeadlessJSONLUsesVersionedEnvelopeAndTerminalResultRecords(t *testing.T) {
	t.Parallel()

	envelope := eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Cursor:    "cursor-1",
		Position: &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{
			Seq: math.MaxUint64,
		}},
		Notice: "working",
	}
	var output bytes.Buffer
	if err := writeHeadlessEnvelope(&output, envelope); err != nil {
		t.Fatal(err)
	}
	usage := eventstream.UsageSnapshot{PromptTokens: 11, TotalTokens: 17}
	if err := writeResult(&output, outputJSONL, runResult{
		SchemaVersion: headlessOutputSchemaVersion,
		Type:          headlessOutputTypeResult,
		SessionID:     "session-1",
		Turn:          controlclient.TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
		Status:        eventstream.LifecycleStateCompleted,
		Output:        "done",
		Cursor:        "cursor-2",
		Usage:         &usage,
		PromptTokens:  11,
	}); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL lines = %d\n%s", len(lines), output.String())
	}
	var first struct {
		SchemaVersion string          `json:"schema_version"`
		Type          string          `json:"type"`
		Envelope      json.RawMessage `json:"envelope"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.SchemaVersion != headlessOutputSchemaVersion || first.Type != headlessOutputTypeEnvelope {
		t.Fatalf("first record = %#v", first)
	}
	var envelopeWire struct {
		Position struct {
			Durable struct {
				Seq string `json:"seq"`
			} `json:"durable"`
		} `json:"position"`
	}
	if err := json.Unmarshal(first.Envelope, &envelopeWire); err != nil {
		t.Fatal(err)
	}
	if envelopeWire.Position.Durable.Seq != "18446744073709551615" {
		t.Fatalf("wire sequence = %q", envelopeWire.Position.Durable.Seq)
	}
	var final runResult
	if err := json.Unmarshal([]byte(lines[1]), &final); err != nil {
		t.Fatal(err)
	}
	if final.SchemaVersion != headlessOutputSchemaVersion ||
		final.Type != headlessOutputTypeResult ||
		final.SessionID != "session-1" ||
		final.Status != eventstream.LifecycleStateCompleted ||
		final.Output != "done" ||
		final.Usage == nil ||
		final.Usage.TotalTokens != 17 {
		t.Fatalf("final record = %#v", final)
	}
}

func TestHeadlessStructuredFailureKeepsStdoutMachineReadable(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	runErr := errors.New("injected headless failure")
	if err := writeHeadlessFailure(&output, outputJSONL, "session-1", runErr); !errors.Is(err, runErr) {
		t.Fatalf("writeHeadlessFailure() = %v", err)
	}
	var record headlessErrorOutput
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.SchemaVersion != headlessOutputSchemaVersion ||
		record.Type != headlessOutputTypeError ||
		record.SessionID != "session-1" ||
		record.Message != runErr.Error() {
		t.Fatalf("error record = %#v", record)
	}
}

func TestRunHeadlessStructuredFormatsEncodePreStackFailures(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"json", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			runErr := run(
				context.Background(),
				[]string{
					"-p", "hello",
					"-format", format,
					"-control-operation-retention", "invalid",
				},
				strings.NewReader(""),
				&stdout,
				&stderr,
			)
			if runErr == nil {
				t.Fatal("run() error = nil")
			}
			var record headlessErrorOutput
			if err := json.Unmarshal(stdout.Bytes(), &record); err != nil {
				t.Fatalf("decode stdout %q: %v", stdout.String(), err)
			}
			if record.SchemaVersion != headlessOutputSchemaVersion ||
				record.Type != headlessOutputTypeError ||
				!strings.Contains(record.Message, "invalid duration") {
				t.Fatalf("structured error = %#v", record)
			}
		})
	}
}

func TestCreateOrResumeHeadlessSessionUsesExistingSessionWithoutCreate(t *testing.T) {
	t.Parallel()

	client := &headlessLifecycleTestClient{
		state: controlclient.SessionState{
			SessionID:    "session-1",
			WorkspaceKey: "durable-workspace",
			CWD:          "/durable/workspace",
		},
	}
	sessionID, err := createOrResumeHeadlessSession(
		context.Background(),
		client,
		session.WorkspaceRef{Key: "current-workspace", CWD: "/current/workspace"},
		"session-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "session-1" || client.createCalls != 0 {
		t.Fatalf("created Session = %q, request = %#v", sessionID, client.created)
	}
}

func TestCreateOrResumeHeadlessSessionCreatesMissingPreferredSession(t *testing.T) {
	t.Parallel()

	client := &headlessLifecycleTestClient{inspectErr: session.ErrSessionNotFound}
	sessionID, err := createOrResumeHeadlessSession(
		context.Background(),
		client,
		session.WorkspaceRef{Key: "current-workspace", CWD: "/current/workspace"},
		"session-new",
	)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "session-new" ||
		client.createCalls != 1 ||
		client.created.PreferredSessionID != "session-new" ||
		client.created.WorkspaceKey != "current-workspace" ||
		client.created.CWD != "/current/workspace" {
		t.Fatalf("created Session = %q, request = %#v", sessionID, client.created)
	}
}

func TestHeadlessSessionRunErrorExplainsClosedResume(t *testing.T) {
	t.Parallel()

	err := headlessSessionRunError("session-closed", controlclient.ErrSessionClosed)
	if !errors.Is(err, controlclient.ErrSessionClosed) ||
		!strings.Contains(err.Error(), "omit -session to create a new Session") {
		t.Fatalf("headlessSessionRunError() = %v", err)
	}
}

func TestRunHeadlessRejectsClosedSessionWithoutRecreatingIt(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: "workspace",
		WorkspaceCWD: workspace,
		SkillDirs:    []string{},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	server, err := local.NewAppServer(stack)
	if err != nil {
		t.Fatal(err)
	}
	clients, _, err := server.Bind(controlclient.Principal{ID: stack.UserID})
	if err != nil {
		t.Fatal(err)
	}
	created, err := clients.Sessions.CreateSession(context.Background(), controlclient.CreateSessionRequest{
		WriteBase:          controlclient.WriteBase{OperationID: "create-closed-headless"},
		PreferredSessionID: "closed-headless",
		WorkspaceKey:       "workspace",
		CWD:                workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = clients.Sessions.CloseSession(context.Background(), controlclient.CloseSessionRequest{
		WriteBase: controlclient.WriteBase{
			OperationID: "close-closed-headless",
			SessionID:   created.SessionID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	resumedID, err := runHeadless(
		context.Background(),
		clients.Sessions,
		stack.Workspace,
		created.SessionID,
		"hello",
		outputJSON,
		&output,
	)
	if resumedID != created.SessionID ||
		!errors.Is(err, controlclient.ErrSessionClosed) ||
		!strings.Contains(err.Error(), "omit -session to create a new Session") {
		t.Fatalf("runHeadless() = Session %q, error %v", resumedID, err)
	}
	if output.Len() != 0 {
		t.Fatalf("runHeadless() wrote result before caller error encoding: %q", output.String())
	}
}

func TestAssemblyFromEnvReturnsParserErrors(t *testing.T) {
	clearSelfAgentEnv(t)
	t.Setenv(acpagentenv.EnvCommand, "/opt/acp-child")
	t.Setenv(acpagentenv.EnvArgsJSON, `{"bad":true}`)
	_, err := assemblyFromEnv()
	if err == nil || !strings.Contains(err.Error(), acpagentenv.EnvArgsJSON) {
		t.Fatalf("assemblyFromEnv() error = %v, want parser error", err)
	}
}

type headlessLifecycleTestClient struct {
	controlclient.SessionClient
	state       controlclient.SessionState
	inspectErr  error
	created     controlclient.CreateSessionRequest
	createCalls int
}

func (client *headlessLifecycleTestClient) InspectSession(
	context.Context,
	controlclient.StateRequest,
) (controlclient.SessionState, error) {
	return client.state, client.inspectErr
}

func (client *headlessLifecycleTestClient) CreateSession(
	_ context.Context,
	request controlclient.CreateSessionRequest,
) (controlclient.CommandResult, error) {
	client.createCalls++
	client.created = request
	return controlclient.CommandResult{
		Outcome:   controlclient.OutcomeCommitted,
		SessionID: request.PreferredSessionID,
	}, nil
}

func TestRunHelpReturnsNil(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := run(context.Background(), []string{"-h"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("run(-h) error = %v, want nil", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "Usage of caelis:") ||
		!strings.Contains(got, "Approval mode: auto-review|manual") ||
		!strings.Contains(got, "Policy profile: workspace-write") ||
		!strings.Contains(got, "Reduce TUI motion") {
		t.Fatalf("stderr = %q, want help usage with approval, policy, and reduced-motion options", got)
	}
}

func TestEnvBoolParsesTUIAnimationSetting(t *testing.T) {
	t.Setenv("CAELIS_TUI_NO_ANIMATION", "true")
	if !envBool("CAELIS_TUI_NO_ANIMATION", false) {
		t.Fatal("envBool(true) = false")
	}
	t.Setenv("CAELIS_TUI_NO_ANIMATION", "0")
	if envBool("CAELIS_TUI_NO_ANIMATION", true) {
		t.Fatal("envBool(0) = true")
	}
	t.Setenv("CAELIS_TUI_NO_ANIMATION", "not-a-bool")
	if !envBool("CAELIS_TUI_NO_ANIMATION", true) {
		t.Fatal("envBool(invalid) did not preserve fallback")
	}
}

func TestRunVersionText(t *testing.T) {
	oldVersion, oldCommit, oldDate := version.Version, version.Commit, version.Date
	version.Version, version.Commit, version.Date = "v1.2.3", "abc123", "2026-07-06T00:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Commit, version.Date = oldVersion, oldCommit, oldDate
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run(version) error = %v", err)
	}
	got := stdout.String()
	for _, want := range []string{"version: v1.2.3", "commit: abc123", "date: 2026-07-06T00:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("version output = %q, want %q", got, want)
		}
	}
}

func TestRunVersionJSON(t *testing.T) {
	oldVersion, oldCommit, oldDate := version.Version, version.Commit, version.Date
	version.Version, version.Commit, version.Date = "v1.2.3", "abc123", "2026-07-06T00:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Commit, version.Date = oldVersion, oldCommit, oldDate
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"version", "-format", "json"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run(version json) error = %v", err)
	}
	var decoded versionResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode version json: %v", err)
	}
	if decoded.Version != "v1.2.3" || decoded.Commit != "abc123" || decoded.Date == "" {
		t.Fatalf("decoded version = %#v", decoded)
	}
}

func TestRunUpdateCheckUsesUpdater(t *testing.T) {
	old := runUpdateOperation
	runUpdateOperation = func(_ context.Context, cfg updater.Config, opts updater.UpdateOptions) (updater.Result, error) {
		if !opts.CheckOnly {
			t.Fatal("update --check did not set CheckOnly")
		}
		if strings.TrimSpace(cfg.StoreDir) == "" {
			t.Fatal("update config StoreDir is empty")
		}
		return updater.Result{
			CurrentVersion: "v1.0.0",
			LatestVersion:  "v1.1.0",
			InstallMethod:  updater.MethodRaw,
			Available:      true,
		}, nil
	}
	t.Cleanup(func() { runUpdateOperation = old })
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"update", "--check"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run(update --check) error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "update available: v1.0.0 -> v1.1.0 (raw)") {
		t.Fatalf("update output = %q", got)
	}
}

func TestRunUpdateDoesNotSupportJSONFormat(t *testing.T) {
	old := runUpdateOperation
	called := false
	runUpdateOperation = func(context.Context, updater.Config, updater.UpdateOptions) (updater.Result, error) {
		called = true
		return updater.Result{}, nil
	}
	t.Cleanup(func() { runUpdateOperation = old })
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(context.Background(), []string{"update", "-format", "json"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("run(update -format json) error = %v, want unsupported flag", err)
	}
	if called {
		t.Fatal("runUpdateOperation was called for unsupported update format flag")
	}
}

func TestDefaultStoreDirUsesHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("home directory unavailable")
	}
	want := filepath.Join(home, ".caelis")
	if got := defaultStoreDir(t.TempDir()); got != want {
		t.Fatalf("defaultStoreDir() = %q, want %q", got, want)
	}
}

func TestParseControlOperationRetention(t *testing.T) {
	if got, err := parseControlOperationRetention(""); err != nil || got != 0 {
		t.Fatalf("empty retention = %v, %v", got, err)
	}
	if got, err := parseControlOperationRetention("720h"); err != nil || got != 30*24*time.Hour {
		t.Fatalf("parsed retention = %v, %v", got, err)
	}
	for _, value := range []string{"invalid", "0", "-1h"} {
		if _, err := parseControlOperationRetention(value); err == nil {
			t.Fatalf("retention %q unexpectedly succeeded", value)
		}
	}
}

func TestPreferredSessionIDDefaultsDifferBetweenInteractiveAndHeadless(t *testing.T) {
	if got := preferredInteractiveSessionID(""); got != "" {
		t.Fatalf("preferredInteractiveSessionID(\"\") = %q, want empty for fresh TUI session", got)
	}
	if got := preferredHeadlessSessionID(""); got != "" {
		t.Fatalf("preferredHeadlessSessionID(\"\") = %q, want empty for fresh headless session", got)
	}
	if got := preferredInteractiveSessionID("sticky"); got != "sticky" {
		t.Fatalf("preferredInteractiveSessionID(\"sticky\") = %q, want sticky", got)
	}
	if got := preferredHeadlessSessionID("sticky"); got != "sticky" {
		t.Fatalf("preferredHeadlessSessionID(\"sticky\") = %q, want sticky", got)
	}
}

func cliTestStoreDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data := []byte(`{"sandbox":{"requested_type":"host"}}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write CLI test config: %v", err)
	}
	return dir
}

func useFakeSandboxCommandsForCLITest(t *testing.T) {
	t.Helper()
	oldSetup := runSandboxSetupCommand
	oldFix := runSandboxFixCommand
	oldReset := runSandboxResetCommand
	fake := func(_ context.Context, _ gatewayapp.Config, format outputFormat, stdout io.Writer) error {
		return writeSandboxStatusResult(stdout, format, sandboxStatusResult{
			RequestedBackend: "host",
			ResolvedBackend:  "host",
			Route:            "host",
		})
	}
	runSandboxSetupCommand = fake
	runSandboxFixCommand = fake
	runSandboxResetCommand = fake
	t.Cleanup(func() {
		runSandboxSetupCommand = oldSetup
		runSandboxFixCommand = oldFix
		runSandboxResetCommand = oldReset
	})
}

func clearSelfAgentEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		acpagentenv.EnvName,
		acpagentenv.EnvDescription,
		acpagentenv.EnvCommand,
		acpagentenv.EnvArgsJSON,
		acpagentenv.EnvLegacyCmd,
		acpagentenv.EnvWorkDir,
	} {
		t.Setenv(key, "")
	}
}

func TestRunDoctorJSONDoesNotLeakToken(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := cliTestStoreDir(t)
	seedCLIModel(t, storeDir, gatewayapp.ModelConfig{
		Provider: "minimax", Model: "MiniMax-M1", Token: "super-secret-token",
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-doctor",
		"-format", "json",
		"-store-dir", storeDir,
		"-workspace-key", "doctor-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(-doctor) error = %v", err)
	}
	if strings.Contains(out.String(), "super-secret-token") {
		t.Fatalf("doctor output leaked token: %q", out.String())
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("doctor json decode error = %v", err)
	}
	if got := report["active_provider"]; got != "minimax" {
		t.Fatalf("active_provider = %#v, want minimax", got)
	}
}

func TestRunACPSubcommandConstructsStdioServer(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := cliTestStoreDir(t)
	profileID := seedCLIModel(t, storeDir, gatewayapp.ModelConfig{
		Provider: "ollama", Model: "llama3",
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := run(ctx, []string{
		"acp",
		"-store-dir", storeDir,
		"-workspace-key", "acp-ws",
		"-workspace-cwd", t.TempDir(),
		"-model-profile", profileID,
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(acp) error = %v; stderr=%q", err, errBuf.String())
	}
}

func TestRunDoctorSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := cliTestStoreDir(t)
	seedCLIModel(t, storeDir, gatewayapp.ModelConfig{
		Provider: "deepseek", Model: "deepseek-v4-pro", Token: "deepseek-secret",
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"doctor",
		"-store-dir", storeDir,
		"-workspace-key", "doctor-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(doctor) error = %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "active_provider: deepseek") {
		t.Fatalf("doctor text = %q, want active provider line", text)
	}
	if strings.Contains(text, "super-secret") {
		t.Fatalf("doctor text leaked secret: %q", text)
	}
}

func seedCLIModel(t *testing.T, storeDir string, model gatewayapp.ModelConfig) string {
	t.Helper()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		StoreDir: storeDir, WorkspaceKey: "cli-seed", WorkspaceCWD: t.TempDir(),
		Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := stack.Connect(model)
	if closeErr := stack.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return profile.ID
}

func TestRunSandboxSetupSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	useFakeSandboxCommandsForCLITest(t)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "setup",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox setup) error = %v; stderr=%q", err, errBuf.String())
	}
	text := out.String()
	for _, want := range []string{
		"sandbox_requested_backend: host",
		"sandbox_resolved_backend: host",
		"sandbox_route: host",
		"sandbox_setup_required: false",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("sandbox setup text = %q, want %q", text, want)
		}
	}
}

func TestRunSandboxSetupSubcommandJSONOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	useFakeSandboxCommandsForCLITest(t)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "setup",
		"-format", "json",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox setup json) error = %v; stderr=%q", err, errBuf.String())
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("sandbox setup json decode error = %v", err)
	}
	if got := report["ResolvedBackend"]; got != "host" {
		t.Fatalf("ResolvedBackend = %#v, want host", got)
	}
	if got := report["Route"]; got != "host" {
		t.Fatalf("Route = %#v, want host", got)
	}
}

func TestRunSandboxSetupSubcommandAcceptsBackendOverride(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	useFakeSandboxCommandsForCLITest(t)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "setup",
		"-sandbox-backend", "host",
		"-store-dir", t.TempDir(),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox setup -sandbox-backend host) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox setup output = %q, want requested host backend", out.String())
	}
}

func TestRunSandboxFixSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	useFakeSandboxCommandsForCLITest(t)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "fix",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox fix) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox fix output = %q, want requested host backend", out.String())
	}
}

func TestRunSandboxResetSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	useFakeSandboxCommandsForCLITest(t)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "reset",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox reset) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox reset output = %q, want requested host backend", out.String())
	}
}

func TestRunSandboxCleanSubcommandAliasesReset(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	useFakeSandboxCommandsForCLITest(t)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "clean",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox clean) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox clean output = %q, want requested host backend", out.String())
	}
}
