package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
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
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/productpaths"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/internal/workspaceidentity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/surfaces/acpserver"
	"github.com/caelis-labs/caelis/surfaces/headless"
	"github.com/google/uuid"
)

type outputFormat string

const (
	outputText         outputFormat = "text"
	outputJSON         outputFormat = "json"
	outputJSONL        outputFormat = "jsonl"
	defaultAppName                  = "caelis"
	defaultPrincipalID              = "local-user"

	dangerouslySkipPermissionsWarning = "DANGER: YOLO mode is active. Tools run directly on the host with no sandbox, human approval, or Guardian review.\nThe built-in destructive-command blacklist remains active, but it is limited and is not a security boundary."
)

type runResult struct {
	SchemaVersion string                     `json:"schema_version"`
	Type          string                     `json:"type"`
	SessionID     string                     `json:"session_id"`
	Turn          appserver.TurnTarget       `json:"turn"`
	Status        string                     `json:"status"`
	StopReason    string                     `json:"stop_reason,omitempty"`
	Output        string                     `json:"output"`
	Cursor        string                     `json:"cursor,omitempty"`
	Usage         *eventstream.UsageSnapshot `json:"usage,omitempty"`
	PromptTokens  int                        `json:"prompt_tokens,omitempty"`
}

type sandboxCommandFunc func(context.Context, gatewayapp.Config, productClientOptions, outputFormat, io.Writer) error
type controlServerFunc func(context.Context, controlserver.Dependencies, controlserver.Config) error
type productClientOpener func(context.Context, gatewayapp.Config, productClientOptions) (*productClients, error)

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

func helpRequested(args []string) bool {
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "-h", "--help", "-help":
			return true
		}
	}
	return false
}

func workspaceAddressFromCWD(cwd string) (string, string, error) {
	workspace, err := workspaceidentity.FromCWD(cwd)
	if err != nil {
		return "", "", fmt.Errorf("cli: resolve current workspace: %w", err)
	}
	return workspace.Key, workspace.CWD, nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) (runErr error) {
	return runWithProductClientOpener(ctx, args, stdin, stdout, stderr, openProductClients)
}

func runWithProductClientOpener(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	openClients productClientOpener,
) (runErr error) {
	if openClients == nil {
		return errors.New("cli: product client opener is required")
	}
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "version") {
		return runVersionSubcommand(args[1:], stdout)
	}
	cwd, err := os.Getwd()
	if err != nil {
		if !helpRequested(args) {
			return fmt.Errorf("cli: resolve current workspace: %w", err)
		}
		// Help only needs enough path context to render flag defaults. It must
		// remain available when the shell's former working directory was removed.
		cwd = "."
	}
	defaultStore := defaultStoreDir(cwd)
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
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
	serviceSubcommand := ""
	if len(args) > 0 && isServiceCommand(args[0]) {
		if len(args) < 2 {
			return errors.New("cli: service requires start, stop, restart, or status")
		}
		switch subcommand := strings.ToLower(strings.TrimSpace(args[1])); subcommand {
		case "start", "stop", "restart", "status":
			serviceSubcommand = subcommand
		default:
			return fmt.Errorf("cli: unknown service subcommand %q", subcommand)
		}
		args = args[2:]
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

	var (
		prompt                     = fs.String("p", "", "Single-shot prompt text")
		format                     = fs.String("format", string(outputText), "Output format: text|json|jsonl")
		sessionID                  = fs.String("session", envOr("CAELIS_SESSION_ID", ""), "Session id")
		storeDir                   = fs.String("store-dir", envOr("CAELIS_STORE_DIR", defaultStoreDir(cwd)), "Store directory")
		dangerouslySkipPermissions = fs.Bool(
			"dangerously-skip-permissions",
			false,
			"DANGEROUS: run tools directly on the host without sandbox or approval review",
		)
		forceInteractive = fs.Bool("interactive", false, "Force interactive local main path")
		noAnimation      = fs.Bool("no-animation", envBool("CAELIS_TUI_NO_ANIMATION", false), "Reduce TUI motion")
		controlURL       = fs.String("control-url", envOr("CAELIS_CONTROL_URL", ""), "Attach to an existing Control Host origin (http://127.0.0.1:7777)")
		embeddedHost     = fs.Bool("embedded", envBool("CAELIS_CONTROL_EMBEDDED", false), "Force single-client in-process Host mode; bypass managed local Host attach")
		controlListen    = fs.String("listen", envOr("CAELIS_CONTROL_LISTEN", "127.0.0.1:7777"), "Control Host HTTP listen address for caelis serve")
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
	workspaceKey, workspaceCWD, err := workspaceAddressFromCWD(cwd)
	if err != nil {
		return err
	}
	if acpSubcommand {
		workspaceKey = envOr(acpagentenv.EnvWorkspaceKey, workspaceKey)
		workspaceCWD = envOr(acpagentenv.EnvWorkspaceCWD, workspaceCWD)
	}
	managedListen := ""
	if strings.TrimSpace(os.Getenv("CAELIS_CONTROL_LISTEN")) != "" {
		managedListen = strings.TrimSpace(*controlListen)
	}
	fs.Visit(func(value *flag.Flag) {
		if value.Name == "listen" {
			managedListen = strings.TrimSpace(*controlListen)
		}
	})
	headlessInput := ""
	headlessFormat := outputText
	headlessMode := false
	headlessResultWritten := false
	headlessSessionForError := strings.TrimSpace(*sessionID)
	headlessCandidate := !acpSubcommand &&
		!controlServerSubcommand &&
		!doctorSubcommand &&
		serviceSubcommand == "" &&
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
	cfg := gatewayapp.Config{
		AppName:                    defaultAppName,
		UserID:                     defaultPrincipalID,
		StoreDir:                   *storeDir,
		WorkspaceKey:               workspaceKey,
		WorkspaceCWD:               workspaceCWD,
		DangerouslySkipPermissions: *dangerouslySkipPermissions,
	}
	if serviceSubcommand != "" {
		if *embeddedHost || strings.TrimSpace(*controlURL) != "" {
			return errors.New("cli: service commands address the managed local service and do not accept --embedded or --control-url")
		}
		if strings.TrimSpace(os.Getenv("CAELIS_CONTROL_TOKEN")) != "" ||
			strings.TrimSpace(*controlTokenFile) != "" {
			return errors.New("cli: service commands use the managed local credential and do not accept custom Control credentials")
		}
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		return runLocalHostCommand(ctx, serviceSubcommand, cfg, outFmt, stdout)
	}
	cfg.Assembly, err = assemblyFromEnv()
	if err != nil {
		return err
	}
	interactiveLaunch := !acpSubcommand && !controlServerSubcommand && !doctorSubcommand && serviceSubcommand == "" && sandboxSubcommand == "" && !headlessMode
	if cfg.DangerouslySkipPermissions && !interactiveLaunch {
		_, _ = fmt.Fprintln(stderr, dangerouslySkipPermissionsWarning)
	}
	if controlServerSubcommand {
		return runControlHost(ctx, cfg, controlserver.Config{
			Address: strings.TrimSpace(*controlListen), Principal: appserver.Principal{ID: defaultPrincipalID},
			TokenFile: strings.TrimSpace(*controlTokenFile), AllowedHosts: splitCommaSeparated(*controlHosts),
			TLSCertFile: strings.TrimSpace(*controlTLSCert), TLSKeyFile: strings.TrimSpace(*controlTLSKey),
		})
	}

	clientMode, err := resolveProductClientMode(*embeddedHost, *controlURL)
	if err != nil {
		return err
	}
	if managedListen != "" || strings.TrimSpace(*controlHosts) != "" ||
		strings.TrimSpace(*controlTLSCert) != "" || strings.TrimSpace(*controlTLSKey) != "" {
		return errors.New("cli: --listen, --control-allowed-hosts, and Control TLS flags require caelis serve")
	}
	if clientMode != productClientModeEmbedded && *dangerouslySkipPermissions {
		return errors.New("cli: --dangerously-skip-permissions requires explicit --embedded mode; attached Hosts cannot enable Host escape mode")
	}
	clientOptions := productClientOptions{
		Mode:             clientMode,
		ControlURL:       strings.TrimSpace(*controlURL),
		Token:            strings.TrimSpace(os.Getenv("CAELIS_CONTROL_TOKEN")),
		TokenFile:        strings.TrimSpace(*controlTokenFile),
		WorkspaceKey:     cfg.WorkspaceKey,
		WorkspaceCWD:     cfg.WorkspaceCWD,
		UserID:           cfg.UserID,
		AppName:          cfg.AppName,
		StoreDir:         cfg.StoreDir,
		ListenAddress:    managedListen,
		SurfaceHostCause: doctorSubcommand || sandboxSubcommand != "",
	}
	if sandboxSubcommand != "" {
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		switch sandboxSubcommand {
		case "setup":
			return runSandboxSetupCommand(ctx, cfg, clientOptions, outFmt, stdout)
		case "fix":
			return runSandboxFixCommand(ctx, cfg, clientOptions, outFmt, stdout)
		case "reset", "clean":
			return runSandboxResetCommand(ctx, cfg, clientOptions, outFmt, stdout)
		}
	}

	product, err := openClients(ctx, cfg, clientOptions)
	if err != nil {
		return sandboxStartupEscapeError(err)
	}
	defer func() {
		if closeErr := product.Close(); runErr == nil && closeErr != nil {
			runErr = closeErr
		}
	}()
	if product.ManagedFallback {
		_, _ = fmt.Fprintln(stderr, "caelis: managed local Host unavailable; using embedded mode for this process")
	}
	if product.EmbeddedChildBridgeUnavailable {
		_, _ = fmt.Fprintln(stderr, "caelis: loopback is unavailable; built-in child agents cannot connect for this process")
	}
	if product.Mode == productClientModeEmbedded && product.stack != nil {
		if doctorSubcommand {
			if err := product.stack.WaitApprovalRecovery(ctx); err != nil {
				return err
			}
		} else {
			product.stack.StartApprovalRecovery(ctx)
		}
	}
	if acpSubcommand {
		agent, err := acpagent.NewFromClients(acpagent.ClientsConfig{
			Clients: product.Clients,
			AppName: product.Workspace.AppName, UserID: product.Workspace.UserID,
			WorkspaceKey: product.Workspace.WorkspaceKey, WorkspaceCWD: product.Workspace.WorkspaceCWD,
		})
		if err != nil {
			return err
		}
		return acpserver.ServeStdio(ctx, agent, stdin, stdout)
	}
	if doctorSubcommand {
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		return runDoctor(ctx, product.Clients.Status, strings.TrimSpace(*sessionID), outFmt, stdout)
	}

	if headlessMode {
		activeSessionID, err := runHeadless(
			ctx,
			product.Clients.Sessions,
			session.WorkspaceRef{Key: product.Workspace.WorkspaceKey, CWD: product.Workspace.WorkspaceCWD},
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
	return runInteractive(ctx, product, preferredInteractiveSessionID(*sessionID), renderModelText(cfg), tuiOptions{
		NoAnimation:                *noAnimation,
		DangerouslySkipPermissions: cfg.DangerouslySkipPermissions,
	}, stdin, stdout, stderr)
}

func resolveProductClientMode(embedded bool, controlURL string) (productClientMode, error) {
	controlURL = strings.TrimSpace(controlURL)
	switch {
	case embedded && controlURL != "":
		return 0, errors.New("cli: --embedded and --control-url are mutually exclusive")
	case embedded:
		return productClientModeEmbedded, nil
	case controlURL != "":
		return productClientModeRemote, nil
	default:
		return productClientModeManaged, nil
	}
}

func isServiceCommand(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "service", "svc", "gateway":
		return true
	default:
		return false
	}
}

func runControlHost(ctx context.Context, cfg gatewayapp.Config, serverConfig controlserver.Config) error {
	ownership, err := acquireProductHostOwnership(cfg.StoreDir)
	if err != nil {
		return err
	}
	defer func() { _ = ownership.Close() }()
	var cleanupChildCredential func() error
	defer func() {
		if cleanupChildCredential != nil {
			_ = cleanupChildCredential()
		}
	}()
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		return sandboxStartupEscapeError(err)
	}
	defer func() { _ = stack.Close() }()
	stack.StartApprovalRecovery(ctx)
	if err := stack.WaitApprovalRecovery(ctx); err != nil {
		return err
	}
	instanceID := uuid.NewString()
	startedAt := time.Now().UTC()
	build := version.BuildInfo()
	serverConfig.ServerInfo = appserver.ServerInfo{
		ServerID: appserver.ServerIdentity, InstanceID: instanceID,
		DistributionVersion: build.Version, BuildID: build.BuildID, BuildKind: build.BuildKind,
		Capabilities: appserver.RequiredManagedHostCapabilities(),
	}
	token := strings.TrimSpace(os.Getenv("CAELIS_CONTROL_TOKEN"))
	tokenFile := strings.TrimSpace(serverConfig.TokenFile)
	childTokenFile := tokenFile
	var authenticator controlserver.Authenticator
	if token != "" {
		if tokenFile != "" {
			return errors.New("configure either CAELIS_CONTROL_TOKEN or a Control token file, not both")
		}
		authenticator, err = controlserver.BearerTokenAuthenticator(token, serverConfig.Principal)
		if err != nil {
			return err
		}
		var childToken string
		childTokenFile, childToken, cleanupChildCredential, err = newEphemeralChildControlCredential()
		if err != nil {
			return err
		}
		childAuthenticator, childAuthErr := controlserver.BearerTokenAuthenticator(childToken, serverConfig.Principal)
		if childAuthErr != nil {
			return childAuthErr
		}
		authenticator = anyControlAuthenticator(authenticator, childAuthenticator)
	} else if tokenFile == "" {
		tokenFile = controlserver.DefaultTokenFile(cfg.StoreDir)
		childTokenFile = tokenFile
	}
	defaultTokenFile := controlserver.DefaultTokenFile(cfg.StoreDir)
	discoveryPath := controlserver.DefaultDiscoveryFile(cfg.StoreDir)
	publishedDiscovery := false
	existingOnListening := serverConfig.OnListening
	serverConfig.OnListening = func(listener controlserver.ListenerInfo) error {
		if existingOnListening != nil {
			if err := existingOnListening(listener); err != nil {
				return err
			}
		}
		stack.SetBuiltInChildControl(listener.Endpoint, childTokenFile)
		if token != "" || filepath.Clean(tokenFile) != filepath.Clean(defaultTokenFile) || !isLoopbackEndpoint(listener.Endpoint) {
			return nil
		}
		info := listener.ServerInfo
		if err := controlserver.PublishDiscoveryRecord(discoveryPath, controlserver.DiscoveryRecord{
			SchemaVersion: controlserver.DiscoverySchemaVersion,
			ServerID:      info.ServerID, InstanceID: info.InstanceID,
			AppName: cfg.AppName, PrincipalID: cfg.UserID, PID: os.Getpid(), Endpoint: listener.Endpoint,
			ProtocolVersion: info.ProtocolVersion, EnvelopeVersion: info.EnvelopeVersion, APIVersion: info.APIVersion,
			DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
			Capabilities: info.Capabilities, Transports: info.Transports, StartedAt: startedAt,
		}); err != nil {
			return err
		}
		publishedDiscovery = true
		return nil
	}
	defer func() {
		if publishedDiscovery {
			_ = controlserver.RemoveDiscoveryRecord(discoveryPath, instanceID)
		}
	}()
	appServer, err := local.NewAppServer(stack)
	if err != nil {
		return err
	}
	serverConfig.Authenticator = authenticator
	serverConfig.TokenFile = tokenFile
	return runControlServerCommand(ctx, controlserver.Dependencies{
		Services: appServer.Services, Lifecycle: stack,
	}, serverConfig)
}

func isLoopbackEndpoint(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	return productpaths.DefaultStoreDir(cwd)
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
	client appserver.SessionClient,
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
	turns, err := appserver.NewSessionTurnClient(client)
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
		appserver.SessionTurnStartRequest{
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
	if !errors.Is(err, appserver.ErrSessionClosed) {
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
	client appserver.SessionClient,
	workspace session.WorkspaceRef,
	preferredSessionID string,
) (string, error) {
	preferredSessionID = strings.TrimSpace(preferredSessionID)
	if preferredSessionID != "" {
		state, err := client.InspectSession(ctx, appserver.StateRequest{
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
	result, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "headless-create-" + uuid.NewString(),
		},
		PreferredSessionID: preferredSessionID,
		WorkspaceKey:       workspace.Key,
		CWD:                workspace.CWD,
	})
	if err != nil {
		return "", err
	}
	if result.Outcome != appserver.OutcomeCommitted &&
		result.Outcome != appserver.OutcomeAccepted {
		return "", fmt.Errorf("cli: create or resume Session outcome is %q", result.Outcome)
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return "", errors.New("cli: create or resume Session returned no Session ID")
	}
	return strings.TrimSpace(result.SessionID), nil
}

func runDoctor(ctx context.Context, statusClient appserver.StatusClient, sessionID string, format outputFormat, stdout io.Writer) error {
	if statusClient == nil {
		return errors.New("cli: status client is unavailable")
	}
	status, err := statusClient.SessionStatus(ctx, appserver.StatusRequest{
		SessionID: strings.TrimSpace(sessionID), Surface: "cli", IncludeDiagnostics: true,
	})
	if err != nil {
		return err
	}
	return writeDoctorResult(stdout, format, doctorResultFromStatus(status))
}

func runSandboxSetupFromConfig(ctx context.Context, cfg gatewayapp.Config, options productClientOptions, format outputFormat, stdout io.Writer) error {
	return withCLIAppServer(ctx, cfg, options, func(clients appserver.AppServerClients) error {
		return runSandboxSetup(ctx, clients.Configuration, clients.Status, format, stdout)
	})
}

func runSandboxSetup(ctx context.Context, client appserver.ConfigurationClient, statusClient appserver.StatusClient, format outputFormat, stdout io.Writer) error {
	if client == nil {
		return errors.New("cli: configuration client is unavailable")
	}
	return runSandboxMutation(ctx, statusClient, client.PrepareSandbox, format, stdout)
}

func runSandboxFixFromConfig(ctx context.Context, cfg gatewayapp.Config, options productClientOptions, format outputFormat, stdout io.Writer) error {
	return withCLIAppServer(ctx, cfg, options, func(clients appserver.AppServerClients) error {
		return runSandboxFix(ctx, clients.Configuration, clients.Status, format, stdout)
	})
}

func runSandboxFix(ctx context.Context, client appserver.ConfigurationClient, statusClient appserver.StatusClient, format outputFormat, stdout io.Writer) error {
	if client == nil {
		return errors.New("cli: configuration client is unavailable")
	}
	return runSandboxMutation(ctx, statusClient, client.RepairSandbox, format, stdout)
}

func runSandboxResetFromConfig(ctx context.Context, cfg gatewayapp.Config, options productClientOptions, format outputFormat, stdout io.Writer) error {
	return withCLIAppServer(ctx, cfg, options, func(clients appserver.AppServerClients) error {
		return runSandboxReset(ctx, clients.Configuration, clients.Status, format, stdout)
	})
}

func runSandboxReset(ctx context.Context, client appserver.ConfigurationClient, statusClient appserver.StatusClient, format outputFormat, stdout io.Writer) error {
	if client == nil {
		return errors.New("cli: configuration client is unavailable")
	}
	return runSandboxMutation(ctx, statusClient, client.ResetSandbox, format, stdout)
}

func runSandboxMutation(
	ctx context.Context,
	statusClient appserver.StatusClient,
	mutate func(context.Context, appserver.SandboxRequest) (appserver.CommandResult, error),
	format outputFormat,
	stdout io.Writer,
) error {
	if statusClient == nil {
		return errors.New("cli: status client is unavailable")
	}
	before, err := statusClient.SessionStatus(ctx, appserver.StatusRequest{Surface: "cli", IncludeDiagnostics: true})
	if err != nil {
		return fmt.Errorf("cli: read Host configuration revision: %w", err)
	}
	expectedRevision := before.Configuration.Revision
	result, operationErr := mutate(ctx, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID:      "cli-sandbox-" + uuid.NewString(),
		ExpectedRevision: &expectedRevision,
	}})
	if operationErr == nil && result.Outcome != appserver.OutcomeCommitted {
		operationErr = fmt.Errorf("cli: sandbox operation outcome is %q: %s", result.Outcome, strings.TrimSpace(result.Detail))
	}
	if result.Outcome == appserver.OutcomeAccepted {
		return &appserver.CommandReceiptError{Receipt: result, Err: operationErr}
	}
	status, statusErr := statusClient.SessionStatus(ctx, appserver.StatusRequest{Surface: "cli", IncludeDiagnostics: true})
	if statusErr != nil {
		return &appserver.CommandReceiptError{Receipt: result, Err: errors.Join(operationErr, statusErr)}
	}
	writeErr := writeSandboxStatusResult(stdout, format, sandboxStatusResultFromStatus(status.SandboxStatus))
	resultErr := errors.Join(operationErr, writeErr)
	if resultErr == nil {
		return nil
	}
	return &appserver.CommandReceiptError{Receipt: result, Err: resultErr}
}

func withCLIAppServer(ctx context.Context, cfg gatewayapp.Config, options productClientOptions, action func(appserver.AppServerClients) error) (runErr error) {
	product, err := openProductClients(ctx, cfg, options)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, product.Close())
	}()
	if action == nil {
		return errors.New("cli: AppServer action is required")
	}
	return action(product.Clients)
}

func runInteractive(ctx context.Context, product *productClients, sessionID string, displayModelText string, options tuiOptions, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	if product == nil {
		return errors.New("cli: product clients are unavailable")
	}
	return runTUI(
		ctx,
		product.Clients,
		strings.TrimSpace(sessionID),
		product.Workspace.WorkspaceKey,
		product.Workspace.WorkspaceCWD,
		product.Workspace.StoreDir,
		displayModelText,
		options,
		stdin,
		stdout,
		stderr,
	)
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
