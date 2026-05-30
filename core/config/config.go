// Package config contains typed runtime configuration contracts without
// filesystem, environment, or UI side effects.
package config

import "time"

type Runtime struct {
	AppName      string         `json:"app_name,omitempty"`
	UserID       string         `json:"user_id,omitempty"`
	WorkspaceKey string         `json:"workspace_key,omitempty"`
	WorkspaceCWD string         `json:"workspace_cwd,omitempty"`
	Model        string         `json:"model,omitempty"`
	Store        Store          `json:"store,omitempty"`
	Sandbox      Sandbox        `json:"sandbox,omitempty"`
	Plugins      []Plugin       `json:"plugins,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
}

type Store struct {
	Backend string         `json:"backend,omitempty"`
	URI     string         `json:"uri,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type Sandbox struct {
	Backend       string   `json:"backend,omitempty"`
	ReadableRoots []string `json:"readable_roots,omitempty"`
	WritableRoots []string `json:"writable_roots,omitempty"`
	Network       string   `json:"network,omitempty"`
	HelperPath    string   `json:"helper_path,omitempty"`
}

type ModelProfile struct {
	ID                     string         `json:"id,omitempty"`
	Alias                  string         `json:"alias,omitempty"`
	ProfileID              string         `json:"profile_id,omitempty"`
	EndpointID             string         `json:"endpoint_id,omitempty"`
	Provider               string         `json:"provider,omitempty"`
	Model                  string         `json:"model,omitempty"`
	BaseURL                string         `json:"base_url,omitempty"`
	Token                  string         `json:"token,omitempty"`
	TokenEnv               string         `json:"token_env,omitempty"`
	PersistToken           bool           `json:"persist_token,omitempty"`
	AuthType               string         `json:"auth_type,omitempty"`
	HeaderKey              string         `json:"header_key,omitempty"`
	ContextWindowTokens    int            `json:"context_window_tokens,omitempty"`
	MaxOutputTokens        int            `json:"max_output_tokens,omitempty"`
	ReasoningEffort        string         `json:"reasoning_effort,omitempty"`
	DefaultReasoningEffort string         `json:"default_reasoning_effort,omitempty"`
	ReasoningMode          string         `json:"reasoning_mode,omitempty"`
	ReasoningLevels        []string       `json:"reasoning_levels,omitempty"`
	Timeout                time.Duration  `json:"timeout,omitempty"`
	Meta                   map[string]any `json:"meta,omitempty"`
}

type Plugin struct {
	ID      string         `json:"id,omitempty"`
	Source  string         `json:"source,omitempty"`
	Enabled bool           `json:"enabled,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}
