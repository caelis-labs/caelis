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
	"strings"

	coreconfig "github.com/OnslaughtSnail/caelis/core/config"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	applocal "github.com/OnslaughtSnail/caelis/internal/app/local"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	coreacpserver "github.com/OnslaughtSnail/caelis/internal/surface/acpserver"
	coreheadless "github.com/OnslaughtSnail/caelis/internal/surface/headless"
)

type outputFormat string

const (
	outputText outputFormat = "text"
	outputJSON outputFormat = "json"
)

type runResult struct {
	SessionID    string `json:"session_id"`
	Output       string `json:"output"`
	PromptTokens int    `json:"prompt_tokens,omitempty"`
}

type doctorResult struct {
	AppName                 string   `json:"app_name,omitempty"`
	UserID                  string   `json:"user_id,omitempty"`
	WorkspaceKey            string   `json:"workspace_key,omitempty"`
	WorkspaceCWD            string   `json:"workspace_cwd,omitempty"`
	ActiveProvider          string   `json:"active_provider,omitempty"`
	ActiveModel             string   `json:"active_model,omitempty"`
	ActiveModelAlias        string   `json:"active_model_alias,omitempty"`
	ReasoningEffort         string   `json:"reasoning_effort,omitempty"`
	StoreBackend            string   `json:"store_backend,omitempty"`
	StoreDir                string   `json:"store_dir,omitempty"`
	SandboxRequestedBackend string   `json:"sandbox_requested_backend,omitempty"`
	SandboxResolvedBackend  string   `json:"sandbox_resolved_backend,omitempty"`
	SandboxRoute            string   `json:"sandbox_route,omitempty"`
	SandboxSetupRequired    bool     `json:"sandbox_setup_required,omitempty"`
	SandboxSetupError       string   `json:"sandbox_setup_error,omitempty"`
	SandboxMarkerCurrent    bool     `json:"sandbox_setup_marker_current,omitempty"`
	SandboxMarkerReason     string   `json:"sandbox_setup_marker_reason,omitempty"`
	Warnings                []string `json:"warnings,omitempty"`
}

type sandboxStatusResult struct {
	RequestedBackend   string
	ResolvedBackend    string
	Route              string
	SetupRequired      bool
	SetupError         string
	SetupMarkerCurrent bool
	SetupMarkerReason  string
}

type cliConfig struct {
	AppName        string
	UserID         string
	StoreDir       string
	WorkspaceKey   string
	WorkspaceCWD   string
	PermissionMode string
	ContextWindow  int
	SystemPrompt   string
	Model          cliModelConfig
	Sandbox        cliSandboxConfig
	ExternalAgents []acpexternal.Config
}

type cliModelConfig struct {
	Alias                  string
	Provider               string
	API                    providers.APIType
	Model                  string
	BaseURL                string
	Token                  string
	TokenEnv               string
	AuthType               providers.AuthType
	HeaderKey              string
	MaxOutputTok           int
	ReasoningEffort        string
	DefaultReasoningEffort string
	ReasoningMode          string
	ReasoningLevels        []string
}

type cliSandboxConfig struct {
	RequestedType string
	HelperPath    string
}

func Run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	return run(ctx, args, stdin, stdout, stderr)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	acpSubcommand := len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "acp")
	if acpSubcommand {
		args = args[1:]
	}
	doctorSubcommand := len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "doctor")
	if doctorSubcommand {
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

	cwd, _ := os.Getwd()
	defaultWorkspaceKey := filepath.Base(cwd)
	if defaultWorkspaceKey == "" || defaultWorkspaceKey == "." || defaultWorkspaceKey == string(filepath.Separator) {
		defaultWorkspaceKey = "workspace"
	}

	var (
		prompt           = fs.String("p", "", "Single-shot prompt text")
		format           = fs.String("format", string(outputText), "Output format: text|json")
		appName          = fs.String("app", envOr("CAELIS_APP_NAME", "caelis"), "App name")
		userID           = fs.String("user", envOr("CAELIS_USER_ID", "local-user"), "User id")
		sessionID        = fs.String("session", envOr("CAELIS_SESSION_ID", ""), "Session id")
		storeDir         = fs.String("store-dir", envOr("CAELIS_STORE_DIR", defaultStoreDir(cwd)), "Store directory")
		workspaceKey     = fs.String("workspace-key", envOr("CAELIS_WORKSPACE_KEY", defaultWorkspaceKey), "Workspace key")
		workspaceCWD     = fs.String("workspace-cwd", envOr("CAELIS_WORKSPACE_CWD", cwd), "Workspace cwd")
		systemPrompt     = fs.String("system-prompt", envOr("CAELIS_SYSTEM_PROMPT", ""), "Session override text to append into the assembled system prompt")
		permissionMode   = fs.String("permission-mode", envOr("CAELIS_PERMISSION_MODE", "auto-review"), "Permission mode: auto-review|manual")
		modelAlias       = fs.String("model-alias", envOr("CAELIS_MODEL_ALIAS", ""), "Model alias")
		modelProvider    = fs.String("provider", envOr("CAELIS_MODEL_PROVIDER", ""), "Model provider name")
		modelAPI         = fs.String("api", envOr("CAELIS_MODEL_API", ""), "Model API type")
		modelName        = fs.String("model", envOr("CAELIS_MODEL_NAME", ""), "Model name")
		baseURL          = fs.String("base-url", envOr("CAELIS_BASE_URL", ""), "Provider base URL")
		token            = fs.String("token", envOr("CAELIS_API_TOKEN", ""), "Provider token")
		tokenEnv         = fs.String("token-env", envOr("CAELIS_TOKEN_ENV", ""), "Environment variable for provider token")
		authType         = fs.String("auth-type", envOr("CAELIS_AUTH_TYPE", ""), "Auth type")
		headerKey        = fs.String("header-key", envOr("CAELIS_HEADER_KEY", ""), "Optional auth header key")
		sandboxBackend   = fs.String("sandbox-backend", envOr("CAELIS_SANDBOX_BACKEND", ""), "Sandbox backend: auto|host|bwrap|landlock|seatbelt|windows")
		sandboxHelper    = fs.String("sandbox-helper-path", envOr("CAELIS_SANDBOX_HELPER_PATH", ""), "Sandbox helper executable path")
		contextWindow    = fs.Int("context-window", envInt("CAELIS_CONTEXT_WINDOW", 0), "Context window override")
		maxOutputTokens  = fs.Int("max-output-tokens", envInt("CAELIS_MAX_OUTPUT_TOKENS", 4096), "Max output tokens")
		forceInteractive = fs.Bool("interactive", false, "Force interactive local main path")
		doctor           = fs.Bool("doctor", false, "Print runtime/session/sandbox diagnostics and exit")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unknown arguments: %v", fs.Args())
	}

	cfg, err := normalizeConfig(cliConfig{
		AppName:        *appName,
		UserID:         *userID,
		StoreDir:       *storeDir,
		WorkspaceKey:   *workspaceKey,
		WorkspaceCWD:   *workspaceCWD,
		PermissionMode: *permissionMode,
		ContextWindow:  *contextWindow,
		SystemPrompt:   *systemPrompt,
		Model: cliModelConfig{
			Alias:        *modelAlias,
			Provider:     *modelProvider,
			API:          providers.APIType(strings.TrimSpace(*modelAPI)),
			Model:        *modelName,
			BaseURL:      *baseURL,
			Token:        *token,
			TokenEnv:     *tokenEnv,
			AuthType:     providers.AuthType(strings.TrimSpace(*authType)),
			HeaderKey:    *headerKey,
			MaxOutputTok: *maxOutputTokens,
		},
		Sandbox: cliSandboxConfig{
			RequestedType: strings.TrimSpace(*sandboxBackend),
			HelperPath:    strings.TrimSpace(*sandboxHelper),
		},
	})
	if err != nil {
		return err
	}
	cfg.ExternalAgents = externalACPAgentsFromEnv()
	if acpSubcommand {
		stack, err := newCoreLocalStack(ctx, cfg)
		if err != nil {
			return err
		}
		return coreacpserver.ServeStdio(ctx, coreacpserver.Config{
			Engine:   stack.Engine(),
			Services: stack.Services(),
			AppName:  cfg.AppName,
			UserID:   cfg.UserID,
		}, stdin, stdout)
	}
	if doctorSubcommand || *doctor {
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		stack, err := newCoreLocalStack(ctx, cfg)
		if err != nil {
			return err
		}
		return runDoctor(ctx, stack.Services(), strings.TrimSpace(*sessionID), outFmt, stdout)
	}
	if sandboxSubcommand != "" {
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		stack, err := newCoreLocalStack(ctx, cfg)
		if err != nil {
			return err
		}
		services := stack.Services()
		switch sandboxSubcommand {
		case "setup":
			return runSandboxSetup(ctx, services, outFmt, stdout)
		case "fix":
			return runSandboxFix(ctx, services, outFmt, stdout)
		case "reset", "clean":
			return runSandboxReset(ctx, services, outFmt, stdout)
		}
	}

	stdinTTY := readerIsTTY(stdin)
	input, singleShot, err := resolveTurnInput(*prompt, stdin, stdinTTY, *forceInteractive)
	if err != nil {
		return err
	}
	if singleShot {
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		stack, err := newCoreLocalStack(ctx, cfg)
		if err != nil {
			return err
		}
		return runCoreHeadless(ctx, stack, cfg, preferredHeadlessSessionID(*sessionID), input, outFmt, stdout)
	}
	stack, err := newCoreLocalStack(ctx, cfg)
	if err != nil {
		return err
	}
	return runInteractive(ctx, stack, preferredInteractiveSessionID(*sessionID), cfg, renderModelText(cfg), stdin, stdout, stderr)
}

func externalACPAgentsFromEnv() []acpexternal.Config {
	cmd := strings.TrimSpace(envOr("CAELIS_ACP_SELF_AGENT_CMD", ""))
	if cmd == "" {
		return nil
	}
	name := strings.TrimSpace(envOr("CAELIS_ACP_SELF_AGENT_NAME", "self"))
	if name == "" {
		name = "self"
	}
	return []acpexternal.Config{{
		AgentID:     name,
		AgentName:   name,
		Description: strings.TrimSpace(envOr("CAELIS_ACP_SELF_AGENT_DESC", "")),
		Command:     "bash",
		Args:        []string{"-lc", cmd},
		WorkDir:     strings.TrimSpace(envOr("CAELIS_ACP_SELF_AGENT_WORKDIR", "")),
	}}
}

func defaultStoreDir(cwd string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".caelis")
	}
	return filepath.Join(cwd, ".caelis")
}

func preferredInteractiveSessionID(sessionID string) string {
	return strings.TrimSpace(sessionID)
}

func preferredHeadlessSessionID(sessionID string) string {
	return strings.TrimSpace(sessionID)
}

func newCoreLocalStack(ctx context.Context, cfg cliConfig) (*applocal.Stack, error) {
	sandboxBackend := strings.ToLower(strings.TrimSpace(cfg.Sandbox.RequestedType))
	if sandboxBackend == "" || sandboxBackend == "auto" {
		sandboxBackend = "host"
	}
	modelProvider := coreModelProvider(cfg.Model.Provider, cfg.Model.API)
	settings, err := coreSettingsManager(ctx, cfg, modelProvider)
	if err != nil {
		return nil, err
	}
	return applocal.NewWithContext(ctx, applocal.Config{
		Runtime: coreconfig.Runtime{
			AppName:      cfg.AppName,
			UserID:       cfg.UserID,
			WorkspaceKey: cfg.WorkspaceKey,
			WorkspaceCWD: cfg.WorkspaceCWD,
			Model:        strings.TrimSpace(cfg.Model.Model),
			Store: coreconfig.Store{
				Backend: "jsonl",
				URI:     cfg.StoreDir,
			},
			Meta: map[string]any{
				"permission_mode": strings.TrimSpace(cfg.PermissionMode),
			},
			Sandbox: coreconfig.Sandbox{
				Backend:    sandboxBackend,
				HelperPath: cfg.Sandbox.HelperPath,
			},
		},
		Model: coreconfig.ModelProfile{
			ID:                  firstNonEmptyString(strings.TrimSpace(cfg.Model.Alias), strings.TrimSpace(cfg.Model.Provider), "cli"),
			Alias:               strings.TrimSpace(cfg.Model.Alias),
			Provider:            modelProvider,
			Model:               strings.TrimSpace(cfg.Model.Model),
			BaseURL:             strings.TrimSpace(cfg.Model.BaseURL),
			Token:               strings.TrimSpace(cfg.Model.Token),
			TokenEnv:            strings.TrimSpace(cfg.Model.TokenEnv),
			AuthType:            string(cfg.Model.AuthType),
			HeaderKey:           strings.TrimSpace(cfg.Model.HeaderKey),
			ContextWindowTokens: cfg.ContextWindow,
			MaxOutputTokens:     cfg.Model.MaxOutputTok,
			Meta: map[string]any{
				"cli_provider": strings.TrimSpace(cfg.Model.Provider),
				"cli_api":      string(cfg.Model.API),
			},
		},
		ExternalACPAgents: append([]acpexternal.Config(nil), cfg.ExternalAgents...),
		BuiltinTools:      true,
		Settings:          settings,
	})
}

func coreSettingsManager(ctx context.Context, cfg cliConfig, provider string) (*appsettings.Manager, error) {
	store := appsettings.NewFileStore(cfg.StoreDir)
	doc := appsettings.Document{
		Runtime: coreconfig.Runtime{
			AppName:      cfg.AppName,
			UserID:       cfg.UserID,
			WorkspaceKey: cfg.WorkspaceKey,
			WorkspaceCWD: cfg.WorkspaceCWD,
			Model:        strings.TrimSpace(cfg.Model.Model),
		},
	}
	if strings.TrimSpace(cfg.Model.Model) == "" && strings.TrimSpace(cfg.Model.Provider) == "" && strings.TrimSpace(cfg.Model.BaseURL) == "" {
		return appsettings.NewManager(ctx, store, doc)
	}
	modelCfg := appsettings.ModelConfig{
		Alias:                  firstNonEmptyString(strings.TrimSpace(cfg.Model.Alias), strings.TrimSpace(cfg.Model.Model)),
		Provider:               strings.TrimSpace(provider),
		Model:                  strings.TrimSpace(cfg.Model.Model),
		BaseURL:                strings.TrimSpace(cfg.Model.BaseURL),
		Token:                  strings.TrimSpace(cfg.Model.Token),
		TokenEnv:               strings.TrimSpace(cfg.Model.TokenEnv),
		AuthType:               string(cfg.Model.AuthType),
		HeaderKey:              strings.TrimSpace(cfg.Model.HeaderKey),
		ContextWindowTokens:    cfg.ContextWindow,
		MaxOutputTokens:        cfg.Model.MaxOutputTok,
		ReasoningEffort:        strings.TrimSpace(cfg.Model.ReasoningEffort),
		DefaultReasoningEffort: strings.TrimSpace(cfg.Model.DefaultReasoningEffort),
		ReasoningMode:          strings.TrimSpace(cfg.Model.ReasoningMode),
		ReasoningLevels:        append([]string(nil), cfg.Model.ReasoningLevels...),
	}
	modelCfg = appsettings.NormalizeModelConfig(modelCfg)
	if modelCfg.Provider == "" || modelCfg.Model == "" {
		return appsettings.NewManager(ctx, store, doc)
	}
	doc.Models = appsettings.ModelCatalog{
		DefaultID: modelCfg.ID,
		Configs:   []appsettings.ModelConfig{modelCfg},
	}
	return appsettings.NewManager(ctx, nil, doc)
}

func coreModelProvider(provider string, api providers.APIType) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "openai", "openai_compatible", "openai-compatible":
		return "openai_compatible"
	case "anthropic", "anthropic-compatible":
		return "anthropic"
	case "minimax":
		return "minimax"
	case "gemini":
		return "gemini"
	case "codefree":
		return "codefree"
	case "deepseek":
		return "deepseek"
	case "mimo", "xiaomi":
		return "xiaomi"
	case "openrouter":
		return "openrouter"
	case "volcengine":
		if api == providers.APIVolcengineCoding {
			return "volcengine-coding-plan"
		}
		return "volcengine"
	case "volcengine-coding-plan", "volcengine_coding_plan":
		return "volcengine-coding-plan"
	case "ollama":
		return "ollama"
	default:
		return "openai_compatible"
	}
}

func runCoreHeadless(ctx context.Context, stack *applocal.Stack, cfg cliConfig, sessionID string, input string, format outputFormat, stdout io.Writer) error {
	result, err := coreheadless.RunOnce(ctx, coreheadless.Request{
		Services:           stack.Services(),
		PreferredSessionID: strings.TrimSpace(sessionID),
		Workspace: coresession.Workspace{
			Key: cfg.WorkspaceKey,
			CWD: cfg.WorkspaceCWD,
		},
		Title:          "cli-headless",
		Input:          input,
		Model:          firstNonEmptyString(strings.TrimSpace(cfg.Model.Alias), strings.TrimSpace(cfg.Model.Model)),
		SessionMode:    strings.TrimSpace(cfg.PermissionMode),
		Surface:        "headless",
		ApprovalPolicy: coreheadless.ApprovalPolicyAutoDeny,
	})
	if err != nil {
		return err
	}
	return writeResult(stdout, format, runResult{
		SessionID:    result.Session.SessionID,
		Output:       strings.TrimSpace(result.Output),
		PromptTokens: result.Usage.InputTokens,
	})
}

func runDoctor(ctx context.Context, services appservices.Services, sessionID string, format outputFormat, stdout io.Writer) error {
	view, err := services.Status().View(ctx, appservices.StatusRequest{
		SessionRef: coresession.Ref{SessionID: strings.TrimSpace(sessionID)},
	})
	if err != nil {
		return err
	}
	sandboxStatus, err := services.Sandbox().Status(ctx)
	if err != nil {
		return err
	}
	report := doctorResultFromApp(view, sandboxStatus)
	return writeDoctorResult(stdout, format, report)
}

func runSandboxSetup(ctx context.Context, services appservices.Services, format outputFormat, stdout io.Writer) error {
	status, err := services.Sandbox().Prepare(ctx)
	result := sandboxStatusResultFromApp(status)
	if writeErr := writeSandboxStatusResult(stdout, format, result); writeErr != nil && err == nil {
		err = writeErr
	}
	return err
}

func runSandboxFix(ctx context.Context, services appservices.Services, format outputFormat, stdout io.Writer) error {
	status, err := services.Sandbox().Repair(ctx)
	result := sandboxStatusResultFromApp(status)
	if writeErr := writeSandboxStatusResult(stdout, format, result); writeErr != nil && err == nil {
		err = writeErr
	}
	return err
}

func runSandboxReset(ctx context.Context, services appservices.Services, format outputFormat, stdout io.Writer) error {
	status, err := services.Sandbox().Reset(ctx)
	result := sandboxStatusResultFromApp(status)
	if writeErr := writeSandboxStatusResult(stdout, format, result); writeErr != nil && err == nil {
		err = writeErr
	}
	return err
}

func runInteractive(ctx context.Context, stack *applocal.Stack, sessionID string, cfg cliConfig, displayModelText string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	_ = stderr
	_ = cfg
	return runTUI(ctx, stack, strings.TrimSpace(sessionID), displayModelText, stdin, stdout)
}

func renderModelText(cfg cliConfig) string {
	provider := strings.TrimSpace(cfg.Model.Provider)
	model := strings.TrimSpace(cfg.Model.Model)
	switch {
	case provider == "" && model == "":
		return "not configured"
	case provider == "":
		return model
	case model == "":
		return provider
	default:
		return provider + "/" + model
	}
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
		_, err := fmt.Fprintln(w, formatDoctorResult(result))
		return err
	}
}

func doctorResultFromApp(view appviewmodel.StatusView, sandboxStatus appservices.SandboxStatus) doctorResult {
	result := doctorResult{
		AppName:                 strings.TrimSpace(view.Runtime.AppName),
		UserID:                  strings.TrimSpace(view.Runtime.UserID),
		WorkspaceKey:            strings.TrimSpace(view.Runtime.WorkspaceKey),
		WorkspaceCWD:            strings.TrimSpace(view.Runtime.WorkspaceCWD),
		StoreBackend:            strings.TrimSpace(view.Runtime.StoreBackend),
		StoreDir:                strings.TrimSpace(view.Runtime.StoreURI),
		SandboxRequestedBackend: strings.TrimSpace(sandboxStatus.RequestedBackend),
		SandboxResolvedBackend:  strings.TrimSpace(sandboxStatus.ResolvedBackend),
		SandboxRoute:            strings.TrimSpace(sandboxStatus.Route),
		SandboxSetupRequired:    sandboxStatus.SetupRequired,
		SandboxSetupError:       strings.TrimSpace(sandboxStatus.SetupError),
		SandboxMarkerCurrent:    sandboxStatus.SetupMarkerCurrent,
		SandboxMarkerReason:     strings.TrimSpace(sandboxStatus.SetupMarkerReason),
		ReasoningEffort:         strings.TrimSpace(view.Model.ReasoningEffort),
	}
	if view.Model.Current != nil {
		result.ActiveProvider = strings.TrimSpace(view.Model.Current.Provider)
		result.ActiveModel = strings.TrimSpace(view.Model.Current.Model)
		result.ActiveModelAlias = strings.TrimSpace(firstNonEmptyString(view.Model.Current.Alias, view.Model.Current.ID))
	}
	if result.SandboxSetupError != "" {
		result.Warnings = append(result.Warnings, "sandbox setup error: "+result.SandboxSetupError)
	}
	return result
}

func formatDoctorResult(report doctorResult) string {
	lines := []string{
		fmt.Sprintf("app_name: %s", firstNonEmptyString(report.AppName, "-")),
		fmt.Sprintf("user_id: %s", firstNonEmptyString(report.UserID, "-")),
		fmt.Sprintf("workspace_key: %s", firstNonEmptyString(report.WorkspaceKey, "-")),
		fmt.Sprintf("workspace_cwd: %s", firstNonEmptyString(report.WorkspaceCWD, "-")),
		fmt.Sprintf("active_provider: %s", firstNonEmptyString(report.ActiveProvider, "-")),
		fmt.Sprintf("active_model: %s", firstNonEmptyString(report.ActiveModel, "-")),
		fmt.Sprintf("active_model_alias: %s", firstNonEmptyString(report.ActiveModelAlias, "-")),
		fmt.Sprintf("reasoning_effort: %s", firstNonEmptyString(report.ReasoningEffort, "-")),
		fmt.Sprintf("store_backend: %s", firstNonEmptyString(report.StoreBackend, "-")),
		fmt.Sprintf("store_dir: %s", firstNonEmptyString(report.StoreDir, "-")),
		fmt.Sprintf("sandbox_requested_backend: %s", firstNonEmptyString(report.SandboxRequestedBackend, "-")),
		fmt.Sprintf("sandbox_resolved_backend: %s", firstNonEmptyString(report.SandboxResolvedBackend, "-")),
		fmt.Sprintf("sandbox_route: %s", firstNonEmptyString(report.SandboxRoute, "-")),
		fmt.Sprintf("sandbox_setup_required: %t", report.SandboxSetupRequired),
		fmt.Sprintf("sandbox_setup_error: %s", firstNonEmptyString(report.SandboxSetupError, "-")),
		fmt.Sprintf("sandbox_setup_marker_current: %t", report.SandboxMarkerCurrent),
		fmt.Sprintf("sandbox_setup_marker_reason: %s", firstNonEmptyString(report.SandboxMarkerReason, "-")),
	}
	if len(report.Warnings) > 0 {
		lines = append(lines, "warnings:")
		for _, warning := range report.Warnings {
			lines = append(lines, "  - "+strings.TrimSpace(warning))
		}
	}
	return strings.Join(lines, "\n")
}

func sandboxStatusResultFromApp(status appservices.SandboxStatus) sandboxStatusResult {
	return sandboxStatusResult{
		RequestedBackend:   strings.TrimSpace(status.RequestedBackend),
		ResolvedBackend:    strings.TrimSpace(status.ResolvedBackend),
		Route:              strings.TrimSpace(status.Route),
		SetupRequired:      status.SetupRequired,
		SetupError:         strings.TrimSpace(status.SetupError),
		SetupMarkerCurrent: status.SetupMarkerCurrent,
		SetupMarkerReason:  strings.TrimSpace(status.SetupMarkerReason),
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
	lines := []string{
		fmt.Sprintf("sandbox_requested_backend: %s", firstNonEmptyString(strings.TrimSpace(status.RequestedBackend), "-")),
		fmt.Sprintf("sandbox_resolved_backend: %s", firstNonEmptyString(strings.TrimSpace(status.ResolvedBackend), "-")),
		fmt.Sprintf("sandbox_route: %s", firstNonEmptyString(strings.TrimSpace(status.Route), "-")),
		fmt.Sprintf("sandbox_setup_required: %t", status.SetupRequired),
		fmt.Sprintf("sandbox_setup_error: %s", firstNonEmptyString(strings.TrimSpace(status.SetupError), "-")),
		fmt.Sprintf("sandbox_setup_marker_current: %t", status.SetupMarkerCurrent),
		fmt.Sprintf("sandbox_setup_marker_reason: %s", firstNonEmptyString(strings.TrimSpace(status.SetupMarkerReason), "-")),
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
	case outputJSON:
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
	default:
		return "", fmt.Errorf("invalid format %q, expected text|json", raw)
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

func normalizeConfig(cfg cliConfig) (cliConfig, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Model.Provider))
	switch provider {
	case "", "minimax":
		cfg.Model.Provider = "minimax"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIMiniMax
		}
		if cfg.Model.AuthType == "" {
			cfg.Model.AuthType = providers.AuthBearerToken
		}
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "MINIMAX_API_KEY"
		}
	case "deepseek":
		cfg.Model.Provider = "deepseek"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIDeepSeek
		}
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "DEEPSEEK_API_KEY"
		}
	case "openrouter":
		cfg.Model.Provider = "openrouter"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIOpenRouter
		}
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "OPENROUTER_API_KEY"
		}
	case "mimo", "xiaomi":
		cfg.Model.Provider = "xiaomi"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIMimo
		}
		if strings.TrimSpace(cfg.Model.BaseURL) == "" {
			cfg.Model.BaseURL = "https://api.xiaomimimo.com/v1"
		}
	case "volcengine":
		cfg.Model.Provider = "volcengine"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIVolcengine
		}
		if strings.TrimSpace(cfg.Model.BaseURL) == "" {
			cfg.Model.BaseURL = "https://ark.cn-beijing.volces.com/api/v3"
		}
	case "volcengine-coding-plan", "volcengine_coding_plan":
		cfg.Model.Provider = "volcengine-coding-plan"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIVolcengineCoding
		}
		if strings.TrimSpace(cfg.Model.BaseURL) == "" {
			cfg.Model.BaseURL = "https://ark.cn-beijing.volces.com/api/coding/v3"
		}
	case "openai":
		cfg.Model.Provider = "openai"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIOpenAI
		}
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "OPENAI_API_KEY"
		}
	case "anthropic", "anthropic-compatible":
		cfg.Model.Provider = "anthropic"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIAnthropic
		}
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "ANTHROPIC_API_KEY"
		}
	case "gemini":
		cfg.Model.Provider = "gemini"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIGemini
		}
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "GEMINI_API_KEY"
		}
		if strings.TrimSpace(cfg.Model.BaseURL) == "" {
			cfg.Model.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
		}
	case "ollama":
		cfg.Model.Provider = "ollama"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APIOllama
		}
		cfg.Model.AuthType = providers.AuthNone
	case "codefree":
		cfg.Model.Provider = "codefree"
		if cfg.Model.API == "" {
			cfg.Model.API = providers.APICodeFree
		}
		if strings.TrimSpace(cfg.Model.BaseURL) == "" {
			cfg.Model.BaseURL = "https://www.srdcloud.cn"
		}
		cfg.Model.AuthType = providers.AuthNone
	default:
		if cfg.Model.API == "" {
			return cliConfig{}, fmt.Errorf("provider %q requires --api", cfg.Model.Provider)
		}
	}
	if strings.TrimSpace(cfg.Model.Model) == "" {
		// Allow empty model for interactive TUI — user can configure via /connect wizard.
		cfg.Model.Provider = ""
		cfg.Model.API = ""
		cfg.Model.BaseURL = ""
		cfg.Model.Token = ""
		cfg.Model.TokenEnv = ""
		cfg.Model.AuthType = ""
		cfg.Model.HeaderKey = ""
	}
	return cfg, nil
}
