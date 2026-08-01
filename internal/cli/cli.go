package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/acpagent"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/surfaces/acpserver"
	"github.com/caelis-labs/caelis/surfaces/headless"
	"github.com/google/uuid"
)

type outputFormat string

const (
	outputText  outputFormat = "text"
	outputJSON  outputFormat = "json"
	outputJSONL outputFormat = "jsonl"

	dangerouslySkipPermissionsWarning = "DANGER: YOLO mode is active. Tools run directly on the host with no sandbox, human approval, or Guardian review.\nThe built-in destructive-command blacklist remains active, but it is limited and is not a security boundary."
)

type runResult struct {
	SchemaVersion string                     `json:"schema_version"`
	Type          string                     `json:"type"`
	SessionID     string                     `json:"session_id"`
	Turn          controlclient.TurnTarget   `json:"turn"`
	Status        string                     `json:"status"`
	StopReason    string                     `json:"stop_reason,omitempty"`
	Output        string                     `json:"output"`
	Cursor        string                     `json:"cursor,omitempty"`
	Usage         *eventstream.UsageSnapshot `json:"usage,omitempty"`
	PromptTokens  int                        `json:"prompt_tokens,omitempty"`
}

type doctorResult = gatewayapp.DoctorReport
type sandboxStatusResult = gatewayapp.SandboxStatus
type sandboxCommandFunc func(context.Context, gatewayapp.Config, outputFormat, io.Writer) error
type controlServerFunc func(context.Context, controlserver.Dependencies, controlserver.Config) error

var (
	runSandboxSetupCommand  sandboxCommandFunc = runSandboxSetupFromConfig
	runSandboxFixCommand    sandboxCommandFunc = runSandboxFixFromConfig
	runSandboxResetCommand  sandboxCommandFunc = runSandboxResetFromConfig
	runControlServerCommand controlServerFunc  = controlserver.ListenAndServe
)

func Run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	providers.SetAttributionBuildVersion(version.String())
	return run(ctx, args, stdin, stdout, stderr)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) (runErr error) {
	cwd, _ := os.Getwd()
	defaultStore := defaultStoreDir(cwd)
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "version":
			return runVersionSubcommand(args[1:], stdout)
		case "update":
			return runUpdateSubcommand(ctx, args[1:], defaultStore, stdout, stderr)
		}
	}
	acpSubcommand := len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "acp")
	if acpSubcommand {
		args = args[1:]
	}
	doctorSubcommand := len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "doctor")
	if doctorSubcommand {
		args = args[1:]
	}
	controlServerSubcommand := len(args) > 0 && (strings.EqualFold(strings.TrimSpace(args[0]), "serve") || strings.EqualFold(strings.TrimSpace(args[0]), "server"))
	if controlServerSubcommand {
		args = args[1:]
	}
	sandboxSubcommand := ""
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "sandbox") {
		if len(args) < 2 {
			return fmt.Errorf("unknown sandbox subcommand: %s", strings.Join(args[1:], " "))
		}
		switch subcommand := strings.ToLower(strings.TrimSpace(args[1])); subcommand {
		case "setup", "fix", "reset", "clean":
			sandboxSubcommand = subcommand
		default:
			return fmt.Errorf("unknown sandbox subcommand: %s", strings.Join(args[1:], " "))
		}
		args = args[2:]
	}
	fs := flag.NewFlagSet("caelis", flag.ContinueOnError)
	fs.SetOutput(stderr)

	defaultWorkspaceKey := filepath.Base(cwd)
	if defaultWorkspaceKey == "" || defaultWorkspaceKey == "." || defaultWorkspaceKey == string(filepath.Separator) {
		defaultWorkspaceKey = "workspace"
	}

	var (
		prompt             = fs.String("p", "", "Single-shot prompt text")
		format             = fs.String("format", string(outputText), "Output format: text|json|jsonl")
		appName            = fs.String("app", envOr("CAELIS_APP_NAME", "caelis"), "App name")
		userID             = fs.String("user", envOr("CAELIS_USER_ID", "local-user"), "User id")
		sessionID          = fs.String("session", envOr("CAELIS_SESSION_ID", ""), "Session id")
		storeDir           = fs.String("store-dir", envOr("CAELIS_STORE_DIR", defaultStoreDir(cwd)), "Store directory")
		operationRetention = fs.String(
			"control-operation-retention",
			envOr("CAELIS_CONTROL_OPERATION_RETENTION", ""),
			fmt.Sprintf("Terminal Control operation idempotency window (default %s)", gatewayapp.DefaultControlOperationRetention),
		)
		workspaceKey               = fs.String("workspace-key", envOr("CAELIS_WORKSPACE_KEY", defaultWorkspaceKey), "Workspace key")
		workspaceCWD               = fs.String("workspace-cwd", envOr("CAELIS_WORKSPACE_CWD", cwd), "Workspace cwd")
		systemPrompt               = fs.String("system-prompt", envOr("CAELIS_SYSTEM_PROMPT", ""), "Session override text to append into the assembled system prompt")
		approvalMode               = fs.String("approval-mode", envOr("CAELIS_APPROVAL_MODE", ""), "Approval mode: auto-review|manual")
		policyProfile              = fs.String("policy-profile", envOr("CAELIS_POLICY_PROFILE", ""), "Policy profile: workspace-write")
		dangerouslySkipPermissions = fs.Bool(
			"dangerously-skip-permissions",
			false,
			"DANGEROUS: run tools directly on the host without sandbox or approval review",
		)
		modelProfile     = fs.String("model-profile", envOr("CAELIS_MODEL_PROFILE", ""), "Control-owned ModelProfile ID")
		reasoningEffort  = fs.String("reasoning-effort", envOr("CAELIS_REASONING_EFFORT", ""), "Selected ModelProfile reasoning effort")
		sandboxBackend   = fs.String("sandbox-backend", envOr("CAELIS_SANDBOX_BACKEND", ""), "Sandbox backend override: host or this platform's required backend (legacy auto/default aliases are accepted)")
		sandboxHelper    = fs.String("sandbox-helper-path", envOr("CAELIS_SANDBOX_HELPER_PATH", ""), "Sandbox helper executable path")
		contextWindow    = fs.Int("context-window", envInt("CAELIS_CONTEXT_WINDOW", 0), "Context window override")
		forceInteractive = fs.Bool("interactive", false, "Force interactive local main path")
		noAnimation      = fs.Bool("no-animation", envBool("CAELIS_TUI_NO_ANIMATION", false), "Reduce TUI motion")
		doctor           = fs.Bool("doctor", false, "Print runtime/session/sandbox diagnostics and exit")
		controlListen    = fs.String("listen", envOr("CAELIS_CONTROL_LISTEN", "127.0.0.1:7777"), "Control client HTTP listen address")
		controlTokenFile = fs.String("control-token-file", envOr("CAELIS_CONTROL_TOKEN_FILE", ""), "Path to the platform-secured Control bearer token file")
		controlHosts     = fs.String("control-allowed-hosts", envOr("CAELIS_CONTROL_ALLOWED_HOSTS", ""), "Comma-separated Host allowlist for the Control server")
		controlTLSCert   = fs.String("control-tls-cert", envOr("CAELIS_CONTROL_TLS_CERT", ""), "TLS certificate file for the Control server")
		controlTLSKey    = fs.String("control-tls-key", envOr("CAELIS_CONTROL_TLS_KEY", ""), "TLS private key file for the Control server")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	headlessInput := ""
	headlessFormat := outputText
	headlessMode := false
	headlessResultWritten := false
	headlessSessionForError := strings.TrimSpace(*sessionID)
	headlessCandidate := !acpSubcommand &&
		!controlServerSubcommand &&
		!doctorSubcommand &&
		!*doctor &&
		sandboxSubcommand == "" &&
		!*forceInteractive &&
		(strings.TrimSpace(*prompt) != "" || stdin != nil && !readerIsTTY(stdin))
	if headlessCandidate {
		var err error
		headlessFormat, err = parseOutputFormat(*format)
		if err != nil {
			return err
		}
		defer func() {
			if runErr != nil && !headlessResultWritten {
				runErr = writeHeadlessFailure(
					stdout,
					headlessFormat,
					headlessSessionForError,
					runErr,
				)
			}
		}()
		headlessInput, headlessMode, err = resolveTurnInput(
			*prompt,
			stdin,
			readerIsTTY(stdin),
			false,
		)
		if err != nil {
			return err
		}
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unknown arguments: %v", fs.Args())
	}
	controlOperationRetention, err := parseControlOperationRetention(*operationRetention)
	if err != nil {
		return err
	}

	cfg, err := normalizeConfig(gatewayapp.Config{
		AppName:                    *appName,
		UserID:                     *userID,
		StoreDir:                   *storeDir,
		ControlOperationRetention:  controlOperationRetention,
		WorkspaceKey:               *workspaceKey,
		WorkspaceCWD:               *workspaceCWD,
		ApprovalMode:               *approvalMode,
		PolicyProfile:              *policyProfile,
		DangerouslySkipPermissions: *dangerouslySkipPermissions,
		ContextWindow:              *contextWindow,
		SystemPrompt:               *systemPrompt,
		ModelProfileID:             *modelProfile,
		ModelProfileEffort:         *reasoningEffort,
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: strings.TrimSpace(*sandboxBackend),
			HelperPath:    strings.TrimSpace(*sandboxHelper),
		},
	})
	if err != nil {
		return err
	}
	cfg.Assembly, err = assemblyFromEnv()
	if err != nil {
		return err
	}
	interactiveLaunch := !acpSubcommand && !controlServerSubcommand && !doctorSubcommand && !*doctor && sandboxSubcommand == "" && !headlessMode
	if cfg.DangerouslySkipPermissions && !interactiveLaunch {
		_, _ = fmt.Fprintln(stderr, dangerouslySkipPermissionsWarning)
	}
	if sandboxSubcommand != "" {
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		switch sandboxSubcommand {
		case "setup":
			return runSandboxSetupCommand(ctx, cfg, outFmt, stdout)
		case "fix":
			return runSandboxFixCommand(ctx, cfg, outFmt, stdout)
		case "reset", "clean":
			return runSandboxResetCommand(ctx, cfg, outFmt, stdout)
		}
	}

	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		return sandboxStartupEscapeError(err)
	}
	defer func() {
		if err := stack.Close(); runErr == nil && err != nil {
			runErr = err
		}
	}()
	if acpSubcommand {
		agent, err := acpagent.NewFromStack(stack)
		if err != nil {
			return err
		}
		return acpserver.ServeStdio(ctx, agent, stdin, stdout)
	}
	if controlServerSubcommand {
		stack.StartApprovalRecovery(ctx)
		if err := stack.WaitApprovalRecovery(ctx); err != nil {
			return err
		}
		principal := controlclient.Principal{ID: strings.TrimSpace(*userID)}
		token := strings.TrimSpace(os.Getenv("CAELIS_CONTROL_TOKEN"))
		tokenFile := strings.TrimSpace(*controlTokenFile)
		var authenticator controlserver.Authenticator
		if token != "" {
			if tokenFile != "" {
				return errors.New("configure either CAELIS_CONTROL_TOKEN or a Control token file, not both")
			}
			authenticator, err = controlserver.BearerTokenAuthenticator(token, principal)
			if err != nil {
				return err
			}
		} else if tokenFile == "" {
			tokenFile = controlserver.DefaultTokenFile(cfg.StoreDir)
		}
		appServer, appServerErr := local.NewAppServer(stack)
		if appServerErr != nil {
			return appServerErr
		}
		return runControlServerCommand(ctx, controlserver.Dependencies{
			Services: appServer.Services, TaskStreams: appServer.TaskStreams, Lifecycle: stack,
		}, controlserver.Config{
			Address: strings.TrimSpace(*controlListen), Authenticator: authenticator, Principal: principal,
			TokenFile: tokenFile, AllowedHosts: splitCommaSeparated(*controlHosts),
			TLSCertFile: strings.TrimSpace(*controlTLSCert), TLSKeyFile: strings.TrimSpace(*controlTLSKey),
		})
	}
	if doctorSubcommand || *doctor {
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		return runDoctor(ctx, stack, strings.TrimSpace(*sessionID), outFmt, stdout)
	}

	if headlessMode {
		appServer, err := local.NewAppServer(stack)
		if err != nil {
			return err
		}
		clients, _, err := appServer.Bind(controlclient.Principal{ID: stack.UserID})
		if err != nil {
			return err
		}
		activeSessionID, err := runHeadless(
			ctx,
			clients.Sessions,
			stack.Workspace,
			preferredHeadlessSessionID(*sessionID),
			headlessInput,
			headlessFormat,
			stdout,
		)
		if activeSessionID != "" {
			headlessSessionForError = activeSessionID
		}
		headlessResultWritten = err == nil
		return err
	}
	return runInteractive(ctx, stack, preferredInteractiveSessionID(*sessionID), cfg, renderModelText(cfg), tuiOptions{
		NoAnimation:                *noAnimation,
		DangerouslySkipPermissions: cfg.DangerouslySkipPermissions,
	}, stdin, stdout, stderr)
}

func assemblyFromEnv() (assembly.ResolvedAssembly, error) {
	agent, err := acpagentenv.SelfAgentFromOS("")
	if err != nil {
		return assembly.ResolvedAssembly{}, err
	}
	if agent == nil {
		return assembly.ResolvedAssembly{}, nil
	}
	return assembly.ResolvedAssembly{
		Agents: []assembly.AgentConfig{*agent},
	}, nil
}

func defaultStoreDir(cwd string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".caelis")
	}
	return filepath.Join(cwd, ".caelis")
}

func splitCommaSeparated(value string) []string {
	var result []string
	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func preferredInteractiveSessionID(sessionID string) string {
	return strings.TrimSpace(sessionID)
}

func preferredHeadlessSessionID(sessionID string) string {
	return strings.TrimSpace(sessionID)
}

func runHeadless(
	ctx context.Context,
	client controlclient.SessionClient,
	workspace session.WorkspaceRef,
	sessionID string,
	input string,
	format outputFormat,
	stdout io.Writer,
) (string, error) {
	if client == nil {
		return "", errors.New("cli: Headless Session client is unavailable")
	}
	if _, err := client.Initialize(ctx); err != nil {
		return "", err
	}
	activeSessionID, err := createOrResumeHeadlessSession(
		ctx,
		client,
		workspace,
		sessionID,
	)
	if err != nil {
		return "", err
	}
	turns, err := controlclient.NewSessionTurnClient(client)
	if err != nil {
		return activeSessionID, err
	}
	headlessOptions := headless.Options{}
	if format == outputJSONL {
		headlessOptions.ObserveEnvelope = func(envelope eventstream.Envelope) error {
			return writeHeadlessEnvelope(stdout, envelope)
		}
	}
	result, err := headless.RunSessionOnce(
		ctx,
		turns,
		controlclient.SessionTurnStartRequest{
			SessionID: activeSessionID,
			Input:     input,
		},
		headlessOptions,
	)
	if err != nil {
		return activeSessionID, headlessSessionRunError(activeSessionID, err)
	}
	summary := runResult{
		SchemaVersion: headlessOutputSchemaVersion,
		Type:          headlessOutputTypeResult,
		SessionID:     activeSessionID,
		Turn:          result.Target,
		Status:        strings.TrimSpace(result.LifecycleState),
		StopReason:    strings.TrimSpace(result.StopReason),
		Output:        strings.TrimSpace(result.Output),
		Cursor:        strings.TrimSpace(result.LastCursor),
		PromptTokens:  result.PromptTokens,
	}
	if result.Usage != (eventstream.UsageSnapshot{}) {
		usage := result.Usage
		summary.Usage = &usage
	}
	return activeSessionID, writeResult(stdout, format, summary)
}

func headlessSessionRunError(sessionID string, err error) error {
	if !errors.Is(err, controlclient.ErrSessionClosed) {
		return err
	}
	return fmt.Errorf(
		"cli: Session %q is closed; omit -session to create a new Session: %w",
		strings.TrimSpace(sessionID),
		err,
	)
}

func createOrResumeHeadlessSession(
	ctx context.Context,
	client controlclient.SessionClient,
	workspace session.WorkspaceRef,
	preferredSessionID string,
) (string, error) {
	preferredSessionID = strings.TrimSpace(preferredSessionID)
	if preferredSessionID != "" {
		state, err := client.InspectSession(ctx, controlclient.StateRequest{
			SessionID: preferredSessionID,
		})
		switch {
		case err == nil:
			resumedSessionID := strings.TrimSpace(state.SessionID)
			if resumedSessionID == "" {
				return "", errors.New("cli: inspect Session returned no Session ID")
			}
			if resumedSessionID != preferredSessionID {
				return "", fmt.Errorf(
					"cli: inspect Session returned %q for requested Session %q",
					resumedSessionID,
					preferredSessionID,
				)
			}
			return resumedSessionID, nil
		case errors.Is(err, session.ErrSessionNotFound):
		default:
			return "", err
		}
	}
	result, err := client.CreateSession(ctx, controlclient.CreateSessionRequest{
		WriteBase: controlclient.WriteBase{
			OperationID: "headless-create-" + uuid.NewString(),
		},
		PreferredSessionID: preferredSessionID,
		WorkspaceKey:       workspace.Key,
		CWD:                workspace.CWD,
	})
	if err != nil {
		return "", err
	}
	if result.Outcome != controlclient.OutcomeCommitted &&
		result.Outcome != controlclient.OutcomeAccepted {
		return "", fmt.Errorf("cli: create or resume Session outcome is %q", result.Outcome)
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return "", errors.New("cli: create or resume Session returned no Session ID")
	}
	return strings.TrimSpace(result.SessionID), nil
}

func runDoctor(ctx context.Context, stack *gatewayapp.Stack, sessionID string, format outputFormat, stdout io.Writer) error {
	report, err := stack.Doctor(ctx, gatewayapp.DoctorRequest{
		SessionID: strings.TrimSpace(sessionID),
	})
	if err != nil {
		return err
	}
	return writeDoctorResult(stdout, format, report)
}

func runSandboxSetupFromConfig(ctx context.Context, cfg gatewayapp.Config, format outputFormat, stdout io.Writer) error {
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		return err
	}
	return runSandboxSetup(ctx, stack, format, stdout)
}

func runSandboxSetup(ctx context.Context, stack *gatewayapp.Stack, format outputFormat, stdout io.Writer) error {
	status, err := stack.PrepareSandbox(ctx)
	if writeErr := writeSandboxStatusResult(stdout, format, status); writeErr != nil && err == nil {
		err = writeErr
	}
	return err
}

func runSandboxFixFromConfig(ctx context.Context, cfg gatewayapp.Config, format outputFormat, stdout io.Writer) error {
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		return err
	}
	return runSandboxFix(ctx, stack, format, stdout)
}

func runSandboxFix(ctx context.Context, stack *gatewayapp.Stack, format outputFormat, stdout io.Writer) error {
	status, err := stack.RepairSandbox(ctx)
	if writeErr := writeSandboxStatusResult(stdout, format, status); writeErr != nil && err == nil {
		err = writeErr
	}
	return err
}

func runSandboxResetFromConfig(ctx context.Context, cfg gatewayapp.Config, format outputFormat, stdout io.Writer) error {
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		return err
	}
	return runSandboxReset(ctx, stack, format, stdout)
}

func runSandboxReset(ctx context.Context, stack *gatewayapp.Stack, format outputFormat, stdout io.Writer) error {
	status, err := stack.ResetSandbox(ctx)
	if writeErr := writeSandboxStatusResult(stdout, format, status); writeErr != nil && err == nil {
		err = writeErr
	}
	return err
}

func runInteractive(ctx context.Context, stack *gatewayapp.Stack, sessionID string, cfg gatewayapp.Config, displayModelText string, options tuiOptions, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	stack.StartApprovalRecovery(ctx)
	return runTUI(ctx, stack, strings.TrimSpace(sessionID), cfg, displayModelText, options, stdin, stdout, stderr)
}

func renderModelText(cfg gatewayapp.Config) string {
	profileID := strings.TrimSpace(cfg.ModelProfileID)
	if profileID == "" {
		return "not configured"
	}
	return profileID
}

func renderConfiguredModelText(alias string, provider string, model string) string {
	if trimmedAlias := strings.TrimSpace(alias); trimmedAlias != "" {
		return trimmedAlias
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if provider == "" {
		return model
	}
	return provider + "/" + model
}

func writeDoctorResult(w io.Writer, format outputFormat, result doctorResult) error {
	switch format {
	case outputJSON:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(result)
	default:
		_, err := fmt.Fprintln(w, gatewayapp.FormatDoctorText(result))
		return err
	}
}

func writeSandboxStatusResult(w io.Writer, format outputFormat, result sandboxStatusResult) error {
	switch format {
	case outputJSON:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(result)
	default:
		_, err := fmt.Fprintln(w, formatSandboxStatus(result))
		return err
	}
}

func formatSandboxStatus(status sandboxStatusResult) string {
	globalSetup, _ := status.Setup.Check("global")
	setupRequired := status.Setup.Required || status.SetupRequired
	setupError := firstNonEmptyString(status.Setup.Error, globalSetup.Error, status.SetupError)
	setupMarkerCurrent := status.SetupMarkerCurrent || globalSetup.Current
	setupMarkerReason := firstNonEmptyString(globalSetup.Reason, status.SetupMarkerReason)
	lines := []string{
		fmt.Sprintf("sandbox_requested_backend: %s", firstNonEmptyString(strings.TrimSpace(status.RequestedBackend), "-")),
		fmt.Sprintf("sandbox_resolved_backend: %s", firstNonEmptyString(strings.TrimSpace(status.ResolvedBackend), "-")),
		fmt.Sprintf("sandbox_route: %s", firstNonEmptyString(strings.TrimSpace(status.Route), "-")),
		fmt.Sprintf("sandbox_full_access_mode: %t", status.FullAccessMode),
		fmt.Sprintf("sandbox_security_summary: %s", firstNonEmptyString(strings.TrimSpace(status.SecuritySummary), "-")),
		fmt.Sprintf("sandbox_setup_required: %t", setupRequired),
		fmt.Sprintf("sandbox_setup_error: %s", firstNonEmptyString(strings.TrimSpace(setupError), "-")),
		fmt.Sprintf("sandbox_setup_marker_current: %t", setupMarkerCurrent),
		fmt.Sprintf("sandbox_setup_marker_reason: %s", firstNonEmptyString(strings.TrimSpace(setupMarkerReason), "-")),
	}
	return strings.Join(lines, "\n")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeResult(w io.Writer, format outputFormat, result runResult) error {
	switch format {
	case outputJSON, outputJSONL:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(result)
	default:
		if strings.TrimSpace(result.Output) == "" {
			return nil
		}
		_, err := fmt.Fprintln(w, result.Output)
		return err
	}
}

func resolveInput(prompt string, stdin io.Reader, stdinTTY bool) (string, bool, error) {
	if trimmed := strings.TrimSpace(prompt); trimmed != "" {
		return trimmed, true, nil
	}
	if !stdinTTY {
		buf, err := io.ReadAll(stdin)
		if err != nil {
			return "", false, err
		}
		trimmed := strings.TrimSpace(string(buf))
		if trimmed == "" {
			return "", false, fmt.Errorf("stdin prompt is empty")
		}
		return trimmed, true, nil
	}
	return "", false, nil
}

func resolveTurnInput(prompt string, stdin io.Reader, stdinTTY bool, forceInteractive bool) (string, bool, error) {
	if forceInteractive {
		return "", false, nil
	}
	input, singleShot, err := resolveInput(prompt, stdin, stdinTTY)
	if err != nil {
		return "", false, err
	}
	if singleShot {
		return input, true, nil
	}
	// TTY with no prompt → default to interactive TUI
	if stdinTTY {
		return "", false, nil
	}
	return "", false, nil
}

func parseOutputFormat(raw string) (outputFormat, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(outputText):
		return outputText, nil
	case string(outputJSON):
		return outputJSON, nil
	case string(outputJSONL):
		return outputJSONL, nil
	default:
		return "", fmt.Errorf("invalid format %q, expected text|json|jsonl", raw)
	}
}

func isTTY(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func readerIsTTY(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	return isTTY(file)
}

func envOr(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		var parsed int
		if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func splitNonEmptyCSV(value string) []string {
	values := strings.Split(value, ",")
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func parseControlOperationRetention(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	retention, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid control operation retention %q: %w", value, err)
	}
	if retention <= 0 {
		return 0, errors.New("control operation retention must be greater than zero")
	}
	return retention, nil
}

func normalizeConfig(cfg gatewayapp.Config) (gatewayapp.Config, error) {
	cfg.ModelProfileID = strings.ToLower(strings.TrimSpace(cfg.ModelProfileID))
	cfg.ModelProfileEffort = strings.ToLower(strings.TrimSpace(cfg.ModelProfileEffort))
	return cfg, nil
}

func sandboxStartupEscapeError(err error) error {
	if err == nil {
		return nil
	}
	var unavailable *sandbox.BackendUnavailableError
	if !errors.As(err, &unavailable) {
		return err
	}
	return fmt.Errorf(
		"%w Escape option: restart with --dangerously-skip-permissions to run directly on the host. WARNING: this disables sandbox isolation, human approval, and Guardian review; the remaining destructive-command blacklist is limited and is not a security boundary",
		err,
	)
}
