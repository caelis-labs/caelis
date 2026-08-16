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

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	"github.com/caelis-labs/caelis/internal/gatewayapptest"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/internal/updater"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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
		"serve", "--store-dir", cliTestStoreDir(t), "--listen", "127.0.0.1:7777",
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
	var published bool
	storeDir := cliTestStoreDir(t)
	runControlServerCommand = func(_ context.Context, _ controlserver.Dependencies, config controlserver.Config) error {
		captured = config
		info := config.ServerInfo
		info.ProtocolVersion = schema.CurrentProtocolVersion
		info.EnvelopeVersion = appserver.EnvelopeVersion
		info.APIVersion = appserver.HTTPAPIVersion
		info.Transports = []string{"http"}
		if err := config.OnListening(controlserver.ListenerInfo{
			Endpoint: "http://127.0.0.1:7777", Address: "127.0.0.1:7777", ServerInfo: info,
		}); err != nil {
			return err
		}
		path := controlserver.DefaultDiscoveryFile(storeDir)
		if _, err := controlserver.LoadDiscoveryRecord(path); err != nil {
			t.Fatalf("load managed discovery %q: %v", path, err)
		}
		published = true
		return nil
	}
	err := run(context.Background(), []string{"serve", "--store-dir", storeDir}, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Authenticator != nil || captured.Principal.ID != "local-user" || filepath.Clean(captured.TokenFile) != filepath.Clean(controlserver.DefaultTokenFile(storeDir)) {
		t.Fatalf("control server config = %#v", captured)
	}
	if !published {
		t.Fatal("managed discovery was not published")
	}
	path := controlserver.DefaultDiscoveryFile(storeDir)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed discovery %q remains after shutdown: %v", path, err)
	}
}

func TestProductClientRejectsServeOnlyNetworkFlags(t *testing.T) {
	t.Setenv("CAELIS_CONTROL_LISTEN", "")
	t.Setenv("CAELIS_CONTROL_ALLOWED_HOSTS", "")
	t.Setenv("CAELIS_CONTROL_TLS_CERT", "")
	t.Setenv("CAELIS_CONTROL_TLS_KEY", "")
	for _, arguments := range [][]string{
		{"--listen", "127.0.0.1:0"},
		{"--control-allowed-hosts", "example.test"},
		{"--control-tls-cert", "cert.pem", "--control-tls-key", "key.pem"},
	} {
		err := run(context.Background(), arguments, nil, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "require caelis serve") {
			t.Fatalf("run(%q) error = %v", arguments, err)
		}
	}
}

func TestRunServiceStatusAndStopAreIdempotentWhenServiceIsAbsent(t *testing.T) {
	t.Setenv("CAELIS_CONTROL_URL", "")
	t.Setenv("CAELIS_CONTROL_EMBEDDED", "")
	t.Setenv("CAELIS_CONTROL_TOKEN", "")
	t.Setenv("CAELIS_CONTROL_TOKEN_FILE", "")
	for _, command := range []string{"service", "svc", "gateway"} {
		for _, action := range []string{"status", "stop"} {
			var output bytes.Buffer
			err := run(context.Background(), []string{
				command, action, "--store-dir", t.TempDir(), "--format", "json",
			}, nil, &output, io.Discard)
			if err != nil {
				t.Fatalf("%s %s: %v", command, action, err)
			}
			var result localHostCommandResult
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result != (localHostCommandResult{State: "stopped"}) {
				t.Fatalf("%s %s = %#v", command, action, result)
			}
		}
	}
}

func TestRunServiceCommandRejectsExternalSelectors(t *testing.T) {
	for _, arguments := range [][]string{
		{"service", "status", "--control-url", "http://127.0.0.1:7777"},
		{"svc", "stop", "--embedded"},
	} {
		err := run(context.Background(), arguments, nil, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "managed local service") {
			t.Fatalf("run(%q) error = %v", arguments, err)
		}
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
		Turn:          appserver.TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
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
					"--embedded",
					"--control-url", "http://127.0.0.1:7777",
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
				!strings.Contains(record.Message, "mutually exclusive") {
				t.Fatalf("structured error = %#v", record)
			}
		})
	}
}

func TestCreateOrResumeHeadlessSessionUsesExistingSessionWithoutCreate(t *testing.T) {
	t.Parallel()

	client := &headlessLifecycleTestClient{
		state: appserver.SessionState{
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

	err := headlessSessionRunError("session-closed", appserver.ErrSessionClosed)
	if !errors.Is(err, appserver.ErrSessionClosed) ||
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
	clients, _, err := server.Bind(appserver.Principal{ID: stack.UserID})
	if err != nil {
		t.Fatal(err)
	}
	created, err := clients.Sessions.CreateSession(context.Background(), appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "create-closed-headless"},
		PreferredSessionID: "closed-headless",
		WorkspaceKey:       "workspace",
		CWD:                workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = clients.Sessions.CloseSession(context.Background(), appserver.CloseSessionRequest{
		WriteBase: appserver.WriteBase{
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
		!errors.Is(err, appserver.ErrSessionClosed) ||
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
	appserver.SessionClient
	state       appserver.SessionState
	inspectErr  error
	created     appserver.CreateSessionRequest
	createCalls int
}

func (client *headlessLifecycleTestClient) InspectSession(
	context.Context,
	appserver.StateRequest,
) (appserver.SessionState, error) {
	return client.state, client.inspectErr
}

func (client *headlessLifecycleTestClient) CreateSession(
	_ context.Context,
	request appserver.CreateSessionRequest,
) (appserver.CommandResult, error) {
	client.createCalls++
	client.created = request
	return appserver.CommandResult{
		Outcome:   appserver.OutcomeCommitted,
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
	got := stderr.String()
	if !strings.Contains(got, "Usage of caelis:") ||
		!strings.Contains(got, "Single-shot prompt text") ||
		!strings.Contains(got, "Force single-client in-process Host mode") ||
		!strings.Contains(got, "Reduce TUI motion") {
		t.Fatalf("stderr = %q, want slim help usage", got)
	}
	for _, retired := range []string{
		"-app ", "-user ", "-workspace-key ", "-workspace-cwd ",
		"-sandbox-backend ", "-sandbox-helper-path ", "-context-window ",
		"-model-profile ", "-reasoning-effort ", "-system-prompt ",
		"-approval-mode ", "-policy-profile ", "-control-operation-retention ", "-doctor",
	} {
		if strings.Contains(got, retired) {
			t.Fatalf("stderr = %q, contains retired option %q", got, retired)
		}
	}
}

func TestRunVersionAndHelpDoNotRequireLiveWorkingDirectory(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	if err := os.Remove(workspace); err != nil {
		t.Fatal(err)
	}

	var versionOutput bytes.Buffer
	if err := run(context.Background(), []string{"version"}, nil, &versionOutput, io.Discard); err != nil {
		t.Fatalf("run(version) from removed cwd: %v", err)
	}
	if versionOutput.Len() == 0 {
		t.Fatal("run(version) from removed cwd produced no output")
	}

	var helpOutput bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, nil, io.Discard, &helpOutput); err != nil {
		t.Fatalf("run(--help) from removed cwd: %v", err)
	}
	if !strings.Contains(helpOutput.String(), "Usage of caelis:") {
		t.Fatalf("help output = %q", helpOutput.String())
	}
}

func TestRunRejectsRetiredOptions(t *testing.T) {
	for _, arguments := range [][]string{
		{"--app", "caelis"},
		{"--user", "local-user"},
		{"--workspace-key", "workspace"},
		{"--workspace-cwd", t.TempDir()},
		{"--sandbox-backend", "host"},
		{"--sandbox-helper-path", "/helper"},
		{"--context-window", "128000"},
		{"--model-profile", "profile"},
		{"--reasoning-effort", "high"},
		{"--system-prompt", "extra"},
		{"--approval-mode", "manual"},
		{"--policy-profile", "workspace-write"},
		{"--control-operation-retention", "720h"},
		{"--doctor"},
	} {
		err := run(context.Background(), arguments, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("run(%q) error = %v, want retired flag rejection", arguments, err)
		}
	}
}

func TestRunDerivesWorkspaceAndIdentityFromProcess(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	t.Setenv("CAELIS_CONTROL_URL", "")
	t.Setenv("CAELIS_CONTROL_EMBEDDED", "")
	stop := errors.New("captured config")
	var captured gatewayapp.Config
	err := runWithProductClientOpener(
		context.Background(),
		[]string{"doctor"},
		nil,
		io.Discard,
		io.Discard,
		func(_ context.Context, cfg gatewayapp.Config, _ productClientOptions) (*productClients, error) {
			captured = cfg
			return nil, stop
		},
	)
	if !errors.Is(err, stop) {
		t.Fatalf("runWithProductClientOpener() error = %v, want capture stop", err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if captured.AppName != defaultAppName || captured.UserID != defaultPrincipalID ||
		captured.WorkspaceCWD != canonicalWorkspace || captured.WorkspaceKey != canonicalWorkspace {
		t.Fatalf("derived CLI config = %#v", captured)
	}
}

func TestWorkspaceAddressFromCWDDoesNotCollideForSameBasename(t *testing.T) {
	root := t.TempDir()
	workspaceA := filepath.Join(root, "a", "repo")
	workspaceB := filepath.Join(root, "b", "repo")
	for _, workspace := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	keyA, cwdA, err := workspaceAddressFromCWD(workspaceA)
	if err != nil {
		t.Fatal(err)
	}
	keyB, cwdB, err := workspaceAddressFromCWD(workspaceB)
	if err != nil {
		t.Fatal(err)
	}
	if keyA == keyB {
		t.Fatalf("same-basename workspace keys collide: %q", keyA)
	}
	if keyA != cwdA || keyB != cwdB {
		t.Fatalf("workspace addresses = (%q, %q), (%q, %q), want canonical CWD keys", keyA, cwdA, keyB, cwdB)
	}
}

func TestACPChildUsesPrivateWorkspaceAddress(t *testing.T) {
	t.Setenv(acpagentenv.EnvWorkspaceKey, "parent-workspace")
	t.Setenv(acpagentenv.EnvWorkspaceCWD, "/parent/workspace")
	t.Setenv("CAELIS_CONTROL_URL", "")
	t.Setenv("CAELIS_CONTROL_EMBEDDED", "")
	stop := errors.New("captured config")
	var captured gatewayapp.Config
	err := runWithProductClientOpener(
		context.Background(),
		[]string{"acp"},
		nil,
		io.Discard,
		io.Discard,
		func(_ context.Context, cfg gatewayapp.Config, _ productClientOptions) (*productClients, error) {
			captured = cfg
			return nil, stop
		},
	)
	if !errors.Is(err, stop) {
		t.Fatalf("runWithProductClientOpener() error = %v, want capture stop", err)
	}
	if captured.WorkspaceKey != "parent-workspace" || captured.WorkspaceCWD != "/parent/workspace" {
		t.Fatalf("ACP child workspace = %#v", captured)
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
	oldBuildID, oldBuildKind := version.BuildID, version.BuildKind
	version.Version, version.Commit, version.Date = "v1.2.3", "abc123", "2026-07-06T00:00:00Z"
	version.BuildID, version.BuildKind = "release-build", version.BuildKindRelease
	t.Cleanup(func() {
		version.Version, version.Commit, version.Date = oldVersion, oldCommit, oldDate
		version.BuildID, version.BuildKind = oldBuildID, oldBuildKind
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run(version) error = %v", err)
	}
	got := stdout.String()
	for _, want := range []string{"version: v1.2.3", "commit: abc123", "date: 2026-07-06T00:00:00Z", "build_id: release-build", "build_kind: release"} {
		if !strings.Contains(got, want) {
			t.Fatalf("version output = %q, want %q", got, want)
		}
	}
}

func TestRunVersionJSON(t *testing.T) {
	oldVersion, oldCommit, oldDate := version.Version, version.Commit, version.Date
	oldBuildID, oldBuildKind := version.BuildID, version.BuildKind
	version.Version, version.Commit, version.Date = "v1.2.3", "abc123", "2026-07-06T00:00:00Z"
	version.BuildID, version.BuildKind = "release-build", version.BuildKindRelease
	t.Cleanup(func() {
		version.Version, version.Commit, version.Date = oldVersion, oldCommit, oldDate
		version.BuildID, version.BuildKind = oldBuildID, oldBuildKind
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
	if decoded.Version != "v1.2.3" || decoded.Commit != "abc123" || decoded.Date == "" || decoded.BuildID != "release-build" || decoded.BuildKind != version.BuildKindRelease {
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
	want := filepath.Join(home, ".caelis-dev", "default")
	if got := defaultStoreDir(t.TempDir()); got != want {
		t.Fatalf("defaultStoreDir() = %q, want %q", got, want)
	}
}

func TestSandboxStartupEscapeErrorSuggestsExplicitDangerousMode(t *testing.T) {
	t.Parallel()

	cause := errors.New("bwrap unavailable")
	backendErr := &sandbox.BackendUnavailableError{
		Backend: sandbox.BackendBwrap,
		Err:     cause,
	}
	wrapped := sandboxStartupEscapeError(backendErr)
	if !errors.Is(wrapped, cause) {
		t.Fatalf("sandboxStartupEscapeError() = %v, want wrapped backend cause", wrapped)
	}
	message := wrapped.Error()
	for _, want := range []string{
		"--dangerously-skip-permissions",
		"disables sandbox isolation, human approval, and Guardian review",
		"not a security boundary",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("sandboxStartupEscapeError() = %q, want %q", message, want)
		}
	}
	ordinary := errors.New("ordinary startup failure")
	if got := sandboxStartupEscapeError(ordinary); !errors.Is(got, ordinary) {
		t.Fatalf("ordinary error = %v, want unchanged identity", got)
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
	fake := func(_ context.Context, _ gatewayapp.Config, _ productClientOptions, format outputFormat, stdout io.Writer) error {
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

type cliStatusClientProbe struct {
	request  appserver.StatusRequest
	status   controlstatus.StatusSnapshot
	err      error
	statuses []controlstatus.StatusSnapshot
	errors   []error
	calls    int
}

func (c *cliStatusClientProbe) SessionStatus(_ context.Context, request appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	c.request = request
	index := c.calls
	c.calls++
	if index < len(c.statuses) || index < len(c.errors) {
		var status controlstatus.StatusSnapshot
		var err error
		if index < len(c.statuses) {
			status = c.statuses[index]
		}
		if index < len(c.errors) {
			err = c.errors[index]
		}
		return status, err
	}
	return c.status, c.err
}

type cliConfigurationClientProbe struct {
	appserver.ConfigurationClient
	action  string
	request appserver.SandboxRequest
	result  appserver.CommandResult
	err     error
}

func (c *cliConfigurationClientProbe) PrepareSandbox(_ context.Context, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	c.action, c.request = "prepare", request
	return c.result, c.err
}

func (c *cliConfigurationClientProbe) RepairSandbox(_ context.Context, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	c.action, c.request = "repair", request
	return c.result, c.err
}

func (c *cliConfigurationClientProbe) ResetSandbox(_ context.Context, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	c.action, c.request = "reset", request
	return c.result, c.err
}

type cliErrorWriter struct{ err error }

func (w cliErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRunDoctorUsesDiagnosticStatusClient(t *testing.T) {
	client := &cliStatusClientProbe{status: controlstatus.StatusSnapshot{
		ModelStatus: controlstatus.StatusModel{Provider: "mimo", Name: "mimo-v2.5-pro"},
	}}
	var out bytes.Buffer
	if err := runDoctor(context.Background(), client, "session-1", outputJSON, &out); err != nil {
		t.Fatal(err)
	}
	if client.request.SessionID != "session-1" || client.request.Surface != "cli" || !client.request.IncludeDiagnostics {
		t.Fatalf("doctor status request = %#v, want diagnostic CLI Session request", client.request)
	}
	var result doctorResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ActiveProvider != "mimo" || result.ActiveModel != "mimo-v2.5-pro" {
		t.Fatalf("doctor result = %#v, want canonical model status", result)
	}
}

func TestDoctorResultPreservesLegacySandboxDiagnostics(t *testing.T) {
	updatedAt := time.Date(2026, time.August, 10, 9, 8, 7, 0, time.FixedZone("CST", 8*60*60))
	status := controlstatus.StatusSnapshot{SandboxStatus: controlstatus.StatusSandbox{Setup: controlstatus.SandboxSetupStatus{
		Required: true,
		Checks: []controlstatus.SandboxSetupCheck{
			{
				Name: "global", Scope: "global", Required: true, Reason: "marker stale", Version: 7,
				Details: map[string]string{
					"runner_hash": "1234567890abcdef", "policy_hash": "abcdef1234567890",
					"offline_user": "offline", "online_user": "online", "owner_user": "owner",
				},
			},
			{
				Name: "workspace", Scope: "workspace", Current: true, Root: `C:\workspace`, UpdatedAt: updatedAt,
				Details: map[string]string{"policy_hash": "fedcba0987654321"},
				Counts:  map[string]int{"read_roots": 5, "write_roots": 2, "deny_read": 3, "deny_write": 4},
			},
		},
	}}}
	result := doctorResultFromStatus(status)
	if result.SandboxSetup == nil || result.SandboxSetupVersion != 7 || result.SandboxSetupMarkerReason != "marker stale" ||
		result.SandboxSetupRunnerHash != "1234567890abcdef" || result.SandboxSetupPolicyHash != "abcdef1234567890" ||
		result.SandboxSetupOfflineUser != "offline" || result.SandboxSetupOnlineUser != "online" || result.SandboxSetupOwnerUser != "owner" ||
		result.SandboxSetupReadRoots != 5 || result.SandboxSetupWriteRoots != 2 || result.SandboxSetupDenyRead != 3 || result.SandboxSetupDenyWrite != 4 ||
		!result.SandboxGlobalSetupRequired || !result.SandboxWorkspaceSetupCurrent || result.SandboxWorkspaceSetupRoot != `C:\workspace` ||
		result.SandboxWorkspaceSetupPolicyHash != "fedcba0987654321" || !result.SandboxWorkspaceSetupUpdatedAt.Equal(updatedAt) {
		t.Fatalf("doctor sandbox diagnostics = %#v", result)
	}
	text := formatDoctorResult(result)
	for _, want := range []string{
		"sandbox_setup_version: 7",
		"sandbox_setup_marker_reason: marker stale",
		"sandbox_setup_runner_hash: 1234567890ab",
		"sandbox_setup_policy_hash: abcdef123456",
		"sandbox_setup_offline_user: offline",
		"sandbox_setup_read_roots: 5",
		"sandbox_workspace_setup_policy_hash: fedcba098765",
		"sandbox_workspace_setup_updated_at: 2026-08-10T01:08:07Z",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor text missing %q:\n%s", want, text)
		}
	}
	if empty := doctorResultFromStatus(controlstatus.StatusSnapshot{}); empty.SandboxSetup != nil {
		t.Fatalf("empty sandbox setup = %#v, want omitted", empty.SandboxSetup)
	}
	sandboxResult := sandboxStatusResultFromStatus(status.SandboxStatus)
	if sandboxResult.SetupVersion != 7 || sandboxResult.SetupRunnerHash != "1234567890abcdef" ||
		sandboxResult.SetupReadRoots != 5 || !sandboxResult.GlobalSetupRequired || !sandboxResult.WorkspaceSetupCurrent ||
		sandboxResult.WorkspaceSetupPolicyHash != "fedcba0987654321" || !sandboxResult.WorkspaceSetupUpdatedAt.Equal(updatedAt) {
		t.Fatalf("sandbox command diagnostics = %#v", sandboxResult)
	}
	raw, err := json.Marshal(sandboxResult)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"SetupVersion", "SetupRunnerHash", "SetupPolicyHash", "SetupOfflineUser", "SetupReadRoots",
		"GlobalSetupRequired", "WorkspaceSetupCurrent", "WorkspaceSetupPolicyHash", "WorkspaceSetupUpdatedAt",
	} {
		if _, ok := wire[field]; !ok {
			t.Fatalf("sandbox command JSON missing legacy field %q: %s", field, raw)
		}
	}
}

func TestSandboxCommandsUseConfigurationClient(t *testing.T) {
	tests := []struct {
		name   string
		action string
		run    func(context.Context, appserver.ConfigurationClient, appserver.StatusClient, outputFormat, io.Writer) error
	}{
		{name: "setup", action: "prepare", run: runSandboxSetup},
		{name: "fix", action: "repair", run: runSandboxFix},
		{name: "reset", action: "reset", run: runSandboxReset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := controlstatus.StatusSnapshot{
				Configuration: controlstatus.StatusConfiguration{Revision: 17},
				SandboxStatus: controlstatus.StatusSandbox{RequestedBackend: "host", ResolvedBackend: "host", Route: "host"},
			}
			client := &cliConfigurationClientProbe{result: appserver.CommandResult{Outcome: appserver.OutcomeCommitted}}
			statusClient := &cliStatusClientProbe{status: status}
			var out bytes.Buffer
			if err := tt.run(context.Background(), client, statusClient, outputText, &out); err != nil {
				t.Fatal(err)
			}
			if client.action != tt.action || client.request.SessionID != "" ||
				client.request.OperationID == "" || client.request.ExpectedRevision == nil || *client.request.ExpectedRevision != 17 {
				t.Fatalf("sandbox client call = action %q request %#v", client.action, client.request)
			}
			if statusClient.calls != 2 {
				t.Fatalf("sandbox status calls = %d, want precondition and observation", statusClient.calls)
			}
			if !strings.Contains(out.String(), "sandbox_resolved_backend: host") {
				t.Fatalf("sandbox output = %q", out.String())
			}
		})
	}
}

func TestSandboxCommandDoesNotObserveAcceptedReceiptAsCommitted(t *testing.T) {
	client := &cliConfigurationClientProbe{result: appserver.CommandResult{Outcome: appserver.OutcomeAccepted}}
	statusClient := &cliStatusClientProbe{status: controlstatus.StatusSnapshot{
		Configuration: controlstatus.StatusConfiguration{Revision: 18},
		SandboxStatus: controlstatus.StatusSandbox{ResolvedBackend: "host"},
	}}
	var out bytes.Buffer
	err := runSandboxSetup(context.Background(), client, statusClient, outputText, &out)
	var receiptErr *appserver.CommandReceiptError
	if !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeAccepted {
		t.Fatalf("accepted receipt error = %#v, %v", receiptErr, err)
	}
	if statusClient.calls != 1 || out.Len() != 0 {
		t.Fatalf("accepted receipt status calls/output = %d/%q, want precondition only", statusClient.calls, out.String())
	}
}

func TestSandboxCommandReadsCanonicalStatusAfterOperationFailure(t *testing.T) {
	tests := []struct {
		name   string
		action string
		run    func(context.Context, appserver.ConfigurationClient, appserver.StatusClient, outputFormat, io.Writer) error
	}{
		{name: "setup", action: "prepare", run: runSandboxSetup},
		{name: "fix", action: "repair", run: runSandboxFix},
		{name: "reset", action: "reset", run: runSandboxReset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operationErr := errors.New("sandbox operation failed")
			client := &cliConfigurationClientProbe{
				result: appserver.CommandResult{Outcome: appserver.OutcomeUnknown}, err: operationErr,
			}
			statusClient := &cliStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
				{Configuration: controlstatus.StatusConfiguration{Revision: 19}},
				{Configuration: controlstatus.StatusConfiguration{Revision: 19}, SandboxStatus: controlstatus.StatusSandbox{
					RequestedBackend: "bwrap", ResolvedBackend: "host", Route: "host", FallbackReason: "bwrap unavailable",
				}},
			}}
			var out bytes.Buffer
			err := tt.run(context.Background(), client, statusClient, outputText, &out)
			var receiptErr *appserver.CommandReceiptError
			if !errors.Is(err, operationErr) || !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeUnknown {
				t.Fatalf("sandbox %s error = %v, want operation error", tt.name, err)
			}
			if client.action != tt.action {
				t.Fatalf("sandbox %s action = %q, want %q", tt.name, client.action, tt.action)
			}
			if statusClient.calls != 2 || statusClient.request.SessionID != "" || statusClient.request.Surface != "cli" || !statusClient.request.IncludeDiagnostics {
				t.Fatalf("sandbox failure status request = %#v calls=%d", statusClient.request, statusClient.calls)
			}
			if !strings.Contains(out.String(), "sandbox_requested_backend: bwrap") || !strings.Contains(out.String(), "sandbox_resolved_backend: host") {
				t.Fatalf("sandbox failure status = %q", out.String())
			}
		})
	}
}

func TestSandboxCommandFailurePreservesStatusAndWriteErrors(t *testing.T) {
	operationErr := errors.New("sandbox setup failed")
	statusErr := errors.New("status unavailable")
	client := &cliConfigurationClientProbe{result: appserver.CommandResult{Outcome: appserver.OutcomeUnknown}, err: operationErr}
	statusClient := &cliStatusClientProbe{
		statuses: []controlstatus.StatusSnapshot{{Configuration: controlstatus.StatusConfiguration{Revision: 23}}, {}},
		errors:   []error{nil, statusErr},
	}
	var out bytes.Buffer
	err := runSandboxSetup(context.Background(), client, statusClient, outputText, &out)
	if !errors.Is(err, operationErr) || !errors.Is(err, statusErr) {
		t.Fatalf("status read failure = %v, want operation and status errors", err)
	}
	if out.Len() != 0 {
		t.Fatalf("status read failure wrote misleading output %q", out.String())
	}

	writeErr := errors.New("output unavailable")
	statusClient = &cliStatusClientProbe{status: controlstatus.StatusSnapshot{
		Configuration: controlstatus.StatusConfiguration{Revision: 23},
		SandboxStatus: controlstatus.StatusSandbox{ResolvedBackend: "host"},
	}}
	err = runSandboxSetup(context.Background(), client, statusClient, outputText, cliErrorWriter{err: writeErr})
	if !errors.Is(err, operationErr) || !errors.Is(err, writeErr) {
		t.Fatalf("status write failure = %v, want operation and write errors", err)
	}
}

func TestWithCLIAppServerClosesHostAfterAction(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	}
	actionErr := errors.New("forced action failure")
	err := withCLIAppServer(context.Background(), cfg, productClientOptions{
		Mode: productClientModeEmbedded, EmbeddedControlEndpoint: roundTripEmbeddedControlFactory(t),
	}, func(clients appserver.AppServerClients) error {
		if err := clients.Validate(); err != nil {
			t.Fatal(err)
		}
		return actionErr
	})
	if !errors.Is(err, actionErr) {
		t.Fatalf("withCLIAppServer() error = %v, want action error", err)
	}
	reopened, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		t.Fatalf("reopen Host after CLI action: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
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
		acpagentenv.EnvWorkspaceKey,
		acpagentenv.EnvWorkspaceCWD,
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
	err := runWithRoundTripEmbeddedControl(t, context.Background(), []string{
		"doctor",
		"--embedded",
		"-format", "json",
		"-store-dir", storeDir,
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

func TestRunDoctorDangerouslySkipPermissionsActivatesVisibleYOLOMode(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := cliTestStoreDir(t)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := runWithRoundTripEmbeddedControl(t, context.Background(), []string{
		"doctor",
		"--embedded",
		"--dangerously-skip-permissions",
		"-format", "json",
		"-store-dir", storeDir,
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(doctor --dangerously-skip-permissions) error = %v", err)
	}
	var report gatewayapp.DoctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.FullAccessMode || !report.HostExecution || report.PolicyProfile != "danger-full-access" {
		t.Fatalf("doctor report = %#v, want active YOLO Host mode", report)
	}
	warning := errBuf.String()
	if !strings.Contains(warning, "YOLO mode is active") || !strings.Contains(warning, "not a security boundary") {
		t.Fatalf("stderr = %q, want explicit YOLO risk warning", warning)
	}
}

func TestRunACPSubcommandConstructsStdioServer(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := cliTestStoreDir(t)
	seedCLIModel(t, storeDir, gatewayapp.ModelConfig{
		Provider: "ollama", Model: "llama3",
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := runWithRoundTripEmbeddedControl(t, ctx, []string{
		"acp",
		"--embedded",
		"-store-dir", storeDir,
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
	err := runWithRoundTripEmbeddedControl(t, context.Background(), []string{
		"doctor",
		"--embedded",
		"-store-dir", storeDir,
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
		StoreDir:                  storeDir,
		WorkspaceKey:              "cli-seed",
		WorkspaceCWD:              t.TempDir(),
		ResolveProviderHTTPClient: gatewayapptest.StaticProviderHTTPClient(model.HTTPClient),
		Sandbox:                   gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profileID, err := gatewayapptest.ConnectModel(context.Background(), stack, model)
	if closeErr := stack.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return profileID
}

func TestRunSandboxSetupSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	useFakeSandboxCommandsForCLITest(t)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox",
		"setup",
		"--embedded",
		"-store-dir", cliTestStoreDir(t),
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
		"sandbox",
		"setup",
		"--embedded",
		"-format", "json",
		"-store-dir", cliTestStoreDir(t),
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

func TestRunSandboxFixSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	useFakeSandboxCommandsForCLITest(t)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox",
		"fix",
		"--embedded",
		"-store-dir", cliTestStoreDir(t),
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
		"sandbox",
		"reset",
		"--embedded",
		"-store-dir", cliTestStoreDir(t),
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
		"sandbox",
		"clean",
		"--embedded",
		"-store-dir", cliTestStoreDir(t),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox clean) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox clean output = %q, want requested host backend", out.String())
	}
}
