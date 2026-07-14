package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/internal/updater"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestRunServeStartsProductControlServer(t *testing.T) {
	t.Setenv("CAELIS_CONTROL_TOKEN", "0123456789abcdef0123456789abcdef0123456789abcdef")
	previous := runControlServerCommand
	t.Cleanup(func() { runControlServerCommand = previous })
	var captured controlserver.Config
	runControlServerCommand = func(_ context.Context, stack *gatewayapp.Stack, config controlserver.Config) error {
		if stack == nil || stack.ControlClient() == nil {
			t.Fatal("serve did not assemble the product Control client")
		}
		captured = config
		return nil
	}
	err := run(context.Background(), []string{
		"serve", "--store-dir", t.TempDir(), "--listen", "127.0.0.1:7777",
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
	runControlServerCommand = func(_ context.Context, _ *gatewayapp.Stack, config controlserver.Config) error {
		captured = config
		return nil
	}
	storeDir := t.TempDir()
	err := run(context.Background(), []string{"serve", "--store-dir", storeDir}, nil, io.Discard, io.Discard)
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
	if _, err := parseOutputFormat("xml"); err == nil {
		t.Fatal("parseOutputFormat(xml) error = nil")
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
		!strings.Contains(got, "Policy profile: workspace-write") {
		t.Fatalf("stderr = %q, want help usage with approval mode and policy profile", got)
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

func TestStreamHandleWritesAssistantTextAndDeniesApproval(t *testing.T) {
	title := "RUN_COMMAND"
	handle := newFakeHandle([]eventstream.Envelope{
		{
			Kind:              eventstream.KindRequestPermission,
			ApprovalRequestID: "approval-1",
			Permission: &schema.RequestPermissionRequest{
				SessionID: "s1",
				ToolCall: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Title:         &title,
				},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "interactive ok"},
			},
		},
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	if err := streamHandle(context.Background(), handle, &out, &errBuf); err != nil {
		t.Fatalf("streamHandle() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "interactive ok") {
		t.Fatalf("stdout = %q", got)
	}
	if got := errBuf.String(); !strings.Contains(got, "denied by default") {
		t.Fatalf("stderr = %q", got)
	}
	if len(handle.submits) != 1 || handle.submits[0].Approval == nil || handle.submits[0].Approval.Approved {
		t.Fatalf("submits = %#v", handle.submits)
	}
}

func TestStreamHandleAppendsPrefixGrowingACPMessageDeltasExactly(t *testing.T) {
	handle := newFakeHandle([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       schema.TextContent{Type: "text", Text: "a"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       schema.TextContent{Type: "text", Text: "ab"},
			},
		},
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	if err := streamHandle(context.Background(), handle, &out, &errBuf); err != nil {
		t.Fatalf("streamHandle() error = %v", err)
	}
	if got, want := out.String(), "a\naab\n"; got != want {
		t.Fatalf("stdout = %q, want cumulative render %q from exact ACP deltas", got, want)
	}
	if errBuf.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errBuf.String())
	}
}

func TestStreamHandleIgnoresAutomaticApprovalReviewEvents(t *testing.T) {
	handle := newFakeHandle([]eventstream.Envelope{
		{
			Kind: eventstream.KindApprovalReview,
			ApprovalReview: &eventstream.ApprovalReview{
				Status: string(gateway.ApprovalReviewStatusInProgress),
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "interactive ok"},
			},
		},
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	if err := streamHandle(context.Background(), handle, &out, &errBuf); err != nil {
		t.Fatalf("streamHandle() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "interactive ok") {
		t.Fatalf("stdout = %q", got)
	}
	if got := errBuf.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
	if len(handle.submits) != 0 {
		t.Fatalf("submits = %#v, want no manual decision for auto-review event", handle.submits)
	}
}

func TestRunDoctorJSONDoesNotLeakToken(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-doctor",
		"-format", "json",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "doctor-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "minimax",
		"-model", "MiniMax-M1",
		"-token", "super-secret-token",
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
	var out bytes.Buffer
	var errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := run(ctx, []string{
		"acp",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "acp-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "ollama",
		"-model", "llama3",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(acp) error = %v; stderr=%q", err, errBuf.String())
	}
}

func TestRunDoctorSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"doctor",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "doctor-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "deepseek",
		"-api", "deepseek",
		"-model", "deepseek-v4-pro",
		"-token-env", "CAELIS_TEST_DOCTOR_TOKEN",
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

type fakeHandle struct {
	events    chan eventstream.Envelope
	submits   []gateway.SubmitRequest
	closed    bool
	cancelled bool
}

func newFakeHandle(events []eventstream.Envelope) *fakeHandle {
	ch := make(chan eventstream.Envelope, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return &fakeHandle{events: ch}
}

func (h *fakeHandle) HandleID() string { return "h1" }
func (h *fakeHandle) RunID() string    { return "r1" }
func (h *fakeHandle) TurnID() string   { return "t1" }
func (h *fakeHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "s1"}
}
func (h *fakeHandle) CreatedAt() time.Time                   { return time.Time{} }
func (h *fakeHandle) ACPEvents() <-chan eventstream.Envelope { return h.events }
func (h *fakeHandle) Submit(_ context.Context, req gateway.SubmitRequest) error {
	h.submits = append(h.submits, req)
	return nil
}
func (h *fakeHandle) Cancel() gateway.CancelResult {
	h.cancelled = true
	return gateway.CancelResult{Status: gateway.CancelStatusCancelled}
}
func (h *fakeHandle) Close() error {
	h.closed = true
	return nil
}
