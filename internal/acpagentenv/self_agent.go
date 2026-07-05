package acpagentenv

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/assembly"
)

const (
	EnvName        = "CAELIS_ACP_SELF_AGENT_NAME"
	EnvDescription = "CAELIS_ACP_SELF_AGENT_DESC"
	EnvCommand     = "CAELIS_ACP_SELF_AGENT_COMMAND"
	EnvArgsJSON    = "CAELIS_ACP_SELF_AGENT_ARGS_JSON"
	EnvLegacyCmd   = "CAELIS_ACP_SELF_AGENT_CMD"
	EnvWorkDir     = "CAELIS_ACP_SELF_AGENT_WORKDIR"
)

type LookupFunc func(string) string

// SelfAgentFromOS reads a full self-agent replacement from process env.
// A configured override replaces the entire self-agent spec; runtime invocation
// args and token env are not merged into it.
func SelfAgentFromOS(defaultDescription string) (*assembly.AgentConfig, error) {
	return SelfAgentFromEnv(os.Getenv, defaultDescription)
}

// SelfAgentFromEnv reads a full self-agent replacement from lookup.
// It returns nil when no override is configured.
func SelfAgentFromEnv(lookup LookupFunc, defaultDescription string) (*assembly.AgentConfig, error) {
	if lookup == nil {
		lookup = os.Getenv
	}
	if command := strings.TrimSpace(lookup(EnvCommand)); command != "" {
		args, err := argsFromJSON(lookup(EnvArgsJSON))
		if err != nil {
			return nil, err
		}
		agent := baseAgent(lookup, defaultDescription, command, args)
		return &agent, nil
	}
	if rawArgs := strings.TrimSpace(lookup(EnvArgsJSON)); rawArgs != "" {
		return nil, fmt.Errorf("%s requires %s", EnvArgsJSON, EnvCommand)
	}
	if cmd := strings.TrimSpace(lookup(EnvLegacyCmd)); cmd != "" {
		command, args := shellCommandSpec(cmd)
		agent := baseAgent(lookup, defaultDescription, command, args)
		return &agent, nil
	}
	return nil, nil
}

func baseAgent(lookup LookupFunc, defaultDescription string, command string, args []string) assembly.AgentConfig {
	name := strings.TrimSpace(lookup(EnvName))
	if name == "" {
		name = "self"
	}
	description := strings.TrimSpace(lookup(EnvDescription))
	if description == "" {
		description = strings.TrimSpace(defaultDescription)
	}
	return assembly.AgentConfig{
		Name:        name,
		Description: description,
		Command:     command,
		Args:        append([]string(nil), args...),
		WorkDir:     strings.TrimSpace(lookup(EnvWorkDir)),
	}
}

func argsFromJSON(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of strings: %w", EnvArgsJSON, err)
	}
	return args, nil
}

func shellCommandSpec(commandLine string) (string, []string) {
	return shellCommandSpecForGOOS(runtime.GOOS, commandLine)
}

func shellCommandSpecForGOOS(goos string, commandLine string) (string, []string) {
	if strings.EqualFold(goos, "windows") {
		return "cmd", []string{"/C", commandLine}
	}
	return "bash", []string{"-lc", commandLine}
}
