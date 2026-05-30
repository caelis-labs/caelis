package local

import (
	"context"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
)

const selfModelTokenEnv = "CAELIS_SELF_MODEL_TOKEN"

func appendDefaultSelfACPAgent(ctx context.Context, runtimeCfg config.Runtime, modelCfg config.ModelProfile, settings *appsettings.Manager, agents []acpexternal.Config) []acpexternal.Config {
	if hasACPAgent(agents, "self") || strings.TrimSpace(runtimeCfg.Store.URI) == "" {
		return agents
	}
	if modelCfg.Model == "" && settings != nil {
		if configured, err := settings.ResolveModel(""); err == nil {
			modelCfg = appsettings.RuntimeModelProfile(configured)
		}
	}
	self := defaultSelfACPAgent(ctx, runtimeCfg, modelCfg)
	if strings.TrimSpace(self.Command) == "" {
		return agents
	}
	out := append([]acpexternal.Config(nil), agents...)
	out = append(out, self)
	return out
}

func defaultSelfACPAgent(ctx context.Context, runtimeCfg config.Runtime, modelCfg config.ModelProfile) acpexternal.Config {
	if ctx == nil {
		ctx = context.Background()
	}
	if cmd := strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_CMD")); cmd != "" {
		name := strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_NAME"))
		if name == "" {
			name = "self"
		}
		return acpexternal.Config{
			AgentID:     name,
			AgentName:   name,
			Description: firstNonEmpty(strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_DESC")), "Caelis self ACP agent"),
			Command:     "bash",
			Args:        []string{"-lc", cmd},
			WorkDir:     strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_WORKDIR")),
		}
	}
	executable, err := os.Executable()
	if err != nil || strings.TrimSpace(executable) == "" {
		executable = os.Args[0]
	}
	args, env := selfRuntimeInvocation(runtimeCfg, modelCfg)
	return acpexternal.Config{
		AgentID:     "self",
		AgentName:   "self",
		Description: "Caelis self ACP agent",
		Command:     strings.TrimSpace(executable),
		Args:        args,
		Env:         sortedEnvList(env),
	}
}

func selfRuntimeInvocation(runtimeCfg config.Runtime, modelCfg config.ModelProfile) ([]string, map[string]string) {
	args := []string{"acp"}
	env := map[string]string{}
	appendFlag := func(name string, value string) {
		if strings.TrimSpace(value) != "" {
			args = append(args, name, strings.TrimSpace(value))
		}
	}
	appendFlag("-app", runtimeCfg.AppName)
	appendFlag("-user", runtimeCfg.UserID)
	appendFlag("-store-dir", runtimeCfg.Store.URI)
	appendFlag("-workspace-key", runtimeCfg.WorkspaceKey)
	appendFlag("-workspace-cwd", runtimeCfg.WorkspaceCWD)
	if mode := stringMeta(runtimeCfg.Meta, "permission_mode"); mode != "" {
		appendFlag("-permission-mode", mode)
	}
	appendFlag("-model-alias", modelCfg.Alias)
	appendFlag("-provider", modelCfg.Provider)
	appendFlag("-api", stringMeta(modelCfg.Meta, "cli_api"))
	appendFlag("-model", modelCfg.Model)
	appendFlag("-base-url", modelCfg.BaseURL)
	if strings.TrimSpace(modelCfg.Token) != "" {
		env[selfModelTokenEnv] = strings.TrimSpace(modelCfg.Token)
		appendFlag("-token-env", selfModelTokenEnv)
	} else {
		appendFlag("-token-env", modelCfg.TokenEnv)
	}
	appendFlag("-auth-type", modelCfg.AuthType)
	appendFlag("-header-key", modelCfg.HeaderKey)
	if modelCfg.ContextWindowTokens > 0 {
		args = append(args, "-context-window", fmt.Sprintf("%d", modelCfg.ContextWindowTokens))
	}
	if modelCfg.MaxOutputTokens > 0 {
		args = append(args, "-max-output-tokens", fmt.Sprintf("%d", modelCfg.MaxOutputTokens))
	}
	if len(env) == 0 {
		env = nil
	}
	return args, env
}

func hasACPAgent(agents []acpexternal.Config, id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return false
	}
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.AgentID), id) || strings.EqualFold(strings.TrimSpace(agent.AgentName), id) {
			return true
		}
	}
	return false
}

func stringMeta(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	switch value := meta[strings.TrimSpace(key)].(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return ""
	}
}

func sortedEnvList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	env = maps.Clone(env)
	keys := make([]string, 0, len(env))
	for key := range env {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}
