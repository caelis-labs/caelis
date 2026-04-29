package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	headlessadapter "github.com/OnslaughtSnail/caelis/gateway/adapter/headless"
	sdkproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/landlock"
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

type doctorResult = gatewayapp.DoctorReport

func main() {
	if landlock.MaybeRunInternalHelper(os.Args[1:]) {
		return
	}
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
		permissionMode   = fs.String("permission-mode", envOr("CAELIS_PERMISSION_MODE", "default"), "Permission mode: default|full_control")
		modelAlias       = fs.String("model-alias", envOr("CAELIS_MODEL_ALIAS", ""), "Model alias")
		modelProvider    = fs.String("provider", envOr("CAELIS_MODEL_PROVIDER", ""), "Model provider name")
		modelAPI         = fs.String("api", envOr("CAELIS_MODEL_API", ""), "Model API type")
		modelName        = fs.String("model", envOr("CAELIS_MODEL_NAME", ""), "Model name")
		baseURL          = fs.String("base-url", envOr("CAELIS_BASE_URL", ""), "Provider base URL")
		token            = fs.String("token", envOr("CAELIS_API_TOKEN", ""), "Provider token")
		tokenEnv         = fs.String("token-env", envOr("CAELIS_TOKEN_ENV", ""), "Environment variable for provider token")
		authType         = fs.String("auth-type", envOr("CAELIS_AUTH_TYPE", ""), "Auth type")
		headerKey        = fs.String("header-key", envOr("CAELIS_HEADER_KEY", ""), "Optional auth header key")
		contextWindow    = fs.Int("context-window", envInt("CAELIS_CONTEXT_WINDOW", 0), "Context window override")
		maxOutputTokens  = fs.Int("max-output-tokens", envInt("CAELIS_MAX_OUTPUT_TOKENS", 4096), "Max output tokens")
		forceInteractive = fs.Bool("interactive", false, "Force interactive local main path")
		doctor           = fs.Bool("doctor", false, "Print runtime/session/sandbox diagnostics and exit")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unknown arguments: %v", fs.Args())
	}

	cfg, err := normalizeConfig(gatewayapp.Config{
		AppName:        *appName,
		UserID:         *userID,
		StoreDir:       *storeDir,
		WorkspaceKey:   *workspaceKey,
		WorkspaceCWD:   *workspaceCWD,
		PermissionMode: *permissionMode,
		ContextWindow:  *contextWindow,
		SystemPrompt:   *systemPrompt,
		Model: gatewayapp.ModelConfig{
			Alias:        *modelAlias,
			Provider:     *modelProvider,
			API:          sdkproviders.APIType(strings.TrimSpace(*modelAPI)),
			Model:        *modelName,
			BaseURL:      *baseURL,
			Token:        *token,
			TokenEnv:     *tokenEnv,
			AuthType:     sdkproviders.AuthType(strings.TrimSpace(*authType)),
			HeaderKey:    *headerKey,
			MaxOutputTok: *maxOutputTokens,
		},
	})
	if err != nil {
		return err
	}
	cfg.Assembly = assemblyFromEnv()
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		return err
	}
	if acpSubcommand {
		agent, err := stack.NewACPAgent()
		if err != nil {
			return err
		}
		return acp.ServeStdio(ctx, agent, stdin, stdout)
	}
	if doctorSubcommand || *doctor {
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		return runDoctor(ctx, stack, strings.TrimSpace(*sessionID), outFmt, stdout)
	}

	stdinTTY := isTTY(os.Stdin)
	input, singleShot, err := resolveTurnInput(*prompt, stdin, stdinTTY, *forceInteractive)
	if err != nil {
		return err
	}
	if singleShot {
		outFmt, err := parseOutputFormat(*format)
		if err != nil {
			return err
		}
		return runHeadless(ctx, stack, preferredHeadlessSessionID(*sessionID), input, outFmt, stdout)
	}
	return runInteractive(ctx, stack, preferredInteractiveSessionID(*sessionID), cfg, renderModelText(cfg), stdin, stdout, stderr)
}

func assemblyFromEnv() sdkplugin.ResolvedAssembly {
	cmd := strings.TrimSpace(envOr("CAELIS_ACP_SELF_AGENT_CMD", ""))
	if cmd == "" {
		return sdkplugin.ResolvedAssembly{}
	}
	name := strings.TrimSpace(envOr("CAELIS_ACP_SELF_AGENT_NAME", "self"))
	if name == "" {
		name = "self"
	}
	return sdkplugin.ResolvedAssembly{
		Agents: []sdkplugin.AgentConfig{{
			Name:        name,
			Description: strings.TrimSpace(envOr("CAELIS_ACP_SELF_AGENT_DESC", "")),
			Command:     "bash",
			Args:        []string{"-lc", cmd},
			WorkDir:     strings.TrimSpace(envOr("CAELIS_ACP_SELF_AGENT_WORKDIR", "")),
		}},
	}
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

func runHeadless(ctx context.Context, stack *gatewayapp.Stack, sessionID string, input string, format outputFormat, stdout io.Writer) error {
	session, err := stack.StartSession(ctx, sessionID, "cli-headless")
	if err != nil {
		return err
	}
	result, err := headlessadapter.RunOnce(ctx, stack.Gateway, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      input,
		Surface:    "headless",
	}, headlessadapter.Options{})
	if err != nil {
		return err
	}
	return writeResult(stdout, format, runResult{
		SessionID:    session.SessionID,
		Output:       strings.TrimSpace(result.Output),
		PromptTokens: result.PromptTokens,
	})
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

func runInteractive(ctx context.Context, stack *gatewayapp.Stack, sessionID string, cfg gatewayapp.Config, displayModelText string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	_ = stderr
	_ = cfg
	return runTUI(ctx, stack, strings.TrimSpace(sessionID), displayModelText, stdin, stdout)
}

func renderModelText(cfg gatewayapp.Config) string {
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

func streamHandle(ctx context.Context, handle appgateway.TurnHandle, stdout io.Writer, stderr io.Writer) error {
	if handle == nil {
		return nil
	}
	defer handle.Close()

	for env := range handle.Events() {
		if env.Err != nil {
			return env.Err
		}
		if env.Event.Kind == appgateway.EventKindApprovalRequested {
			fmt.Fprintln(stderr, "[approval] denied by default")
			if err := handle.Submit(ctx, appgateway.SubmitRequest{
				Kind:     appgateway.SubmissionKindApproval,
				Approval: &appgateway.ApprovalDecision{Approved: false, Outcome: string(appgateway.ApprovalStatusRejected)},
			}); err != nil {
				return err
			}
		}
		if text := appgateway.AssistantText(env.Event); text != "" {
			fmt.Fprintln(stdout, text)
		}
	}
	return nil
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

func normalizeConfig(cfg gatewayapp.Config) (gatewayapp.Config, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Model.Provider))
	switch provider {
	case "", "minimax":
		cfg.Model.Provider = "minimax"
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "MINIMAX_API_KEY"
		}
	case "deepseek":
		cfg.Model.Provider = "deepseek"
		if cfg.Model.API == "" {
			cfg.Model.API = sdkproviders.APIDeepSeek
		}
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "DEEPSEEK_API_KEY"
		}
	case "openai":
		cfg.Model.Provider = "openai"
		if cfg.Model.API == "" {
			cfg.Model.API = sdkproviders.APIOpenAI
		}
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "OPENAI_API_KEY"
		}
	case "anthropic":
		cfg.Model.Provider = "anthropic"
		if cfg.Model.API == "" {
			cfg.Model.API = sdkproviders.APIAnthropic
		}
		if cfg.Model.TokenEnv == "" {
			cfg.Model.TokenEnv = "ANTHROPIC_API_KEY"
		}
	case "ollama":
		cfg.Model.Provider = "ollama"
		if cfg.Model.API == "" {
			cfg.Model.API = sdkproviders.APIOllama
		}
		cfg.Model.AuthType = sdkproviders.AuthNone
	case "codefree":
		cfg.Model.Provider = "codefree"
		if cfg.Model.API == "" {
			cfg.Model.API = sdkproviders.APICodeFree
		}
		if strings.TrimSpace(cfg.Model.BaseURL) == "" {
			cfg.Model.BaseURL = "https://www.srdcloud.cn"
		}
		cfg.Model.AuthType = sdkproviders.AuthNone
	default:
		if cfg.Model.API == "" {
			return gatewayapp.Config{}, fmt.Errorf("provider %q requires --api", cfg.Model.Provider)
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
