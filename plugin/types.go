package plugin

import (
	"context"

	"github.com/OnslaughtSnail/caelis/skill"
)

// Manifest is the Caelis superset plugin manifest. Importers normalize
// harness-specific manifests into this shape before runtime use.
type Manifest struct {
	SchemaVersion string            `json:"schemaVersion,omitempty"`
	Name          string            `json:"name"`
	Version       string            `json:"version,omitempty"`
	Description   string            `json:"description,omitempty"`
	Author        Author            `json:"author,omitempty"`
	Homepage      string            `json:"homepage,omitempty"`
	Repository    string            `json:"repository,omitempty"`
	License       string            `json:"license,omitempty"`
	Keywords      []string          `json:"keywords,omitempty"`
	Contributions Contributions     `json:"contributions,omitempty"`
	Interface     InterfaceMetadata `json:"interface,omitempty"`
}

// Author identifies a plugin author.
type Author struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// InterfaceMetadata contains app-facing metadata. It is not model-critical.
type InterfaceMetadata struct {
	DisplayName       string   `json:"displayName,omitempty"`
	ShortDescription  string   `json:"shortDescription,omitempty"`
	LongDescription   string   `json:"longDescription,omitempty"`
	DeveloperName     string   `json:"developerName,omitempty"`
	Category          string   `json:"category,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`
	DefaultPrompt     []string `json:"defaultPrompt,omitempty"`
	WebsiteURL        string   `json:"websiteURL,omitempty"`
	PrivacyPolicyURL  string   `json:"privacyPolicyURL,omitempty"`
	TermsOfServiceURL string   `json:"termsOfServiceURL,omitempty"`
	BrandColor        string   `json:"brandColor,omitempty"`
	ComposerIcon      string   `json:"composerIcon,omitempty"`
	Logo              string   `json:"logo,omitempty"`
	Screenshots       []string `json:"screenshots,omitempty"`
}

// Contributions are runtime-facing capabilities supplied by a plugin.
type Contributions struct {
	Skills          []SkillBundle  `json:"skills,omitempty"`
	MCPServers      []MCPServer    `json:"mcpServers,omitempty"`
	Agents          []AgentConfig  `json:"agents,omitempty"`
	Modes           []ModeConfig   `json:"modes,omitempty"`
	Configs         []ConfigOption `json:"configs,omitempty"`
	SystemPrompt    string         `json:"systemPrompt,omitempty"`
	PolicyMode      string         `json:"policyMode,omitempty"`
	ExtraReadRoots  []string       `json:"extraReadRoots,omitempty"`
	ExtraWriteRoots []string       `json:"extraWriteRoots,omitempty"`
}

// SkillBundle points at one directory containing skill subdirectories.
type SkillBundle struct {
	Plugin    string   `json:"plugin,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Root      string   `json:"root"`
	Disabled  []string `json:"disabled,omitempty"`
}

// MCPServer declares one MCP server contribution.
type MCPServer struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
}

// AgentConfig declares one external agent contribution.
type AgentConfig struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	WorkDir     string            `json:"workDir,omitempty"`
}

// ModeConfig declares one app-owned session mode.
type ModeConfig struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ConfigOption declares one app-owned session config option.
type ConfigOption struct {
	ID           string               `json:"id"`
	Name         string               `json:"name,omitempty"`
	Description  string               `json:"description,omitempty"`
	Category     string               `json:"category,omitempty"`
	DefaultValue string               `json:"defaultValue,omitempty"`
	Options      []ConfigSelectOption `json:"options,omitempty"`
}

// ConfigSelectOption is one selectable config value.
type ConfigSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ResolveRequest identifies a local plugin root to resolve.
type ResolveRequest struct {
	Root string
}

// InstallRequest identifies a local plugin root to resolve and record.
type InstallRequest struct {
	Root string
}

// Resolved is a normalized plugin plus materialized runtime contributions.
type Resolved struct {
	Manifest   Manifest
	Root       string
	Skills     []skill.Bundle
	MCPServers []MCPServer
	Runtime    RuntimeContributions
}

// RuntimeContributions are materialized capabilities ready for SDK runtime use.
type RuntimeContributions struct {
	Skills          []skill.Bundle
	MCPServers      []MCPServer
	Agents          []AgentConfig
	Modes           []ModeConfig
	Configs         []ConfigOption
	SystemPrompt    string
	PolicyMode      string
	ExtraReadRoots  []string
	ExtraWriteRoots []string
}

// Installed is the durable installed plugin record stored in lock files.
type Installed struct {
	Name            string         `json:"name"`
	Version         string         `json:"version,omitempty"`
	Root            string         `json:"root"`
	ManifestPath    string         `json:"manifestPath,omitempty"`
	Skills          []SkillBundle  `json:"skills,omitempty"`
	MCPServers      []MCPServer    `json:"mcpServers,omitempty"`
	Agents          []AgentConfig  `json:"agents,omitempty"`
	Modes           []ModeConfig   `json:"modes,omitempty"`
	Configs         []ConfigOption `json:"configs,omitempty"`
	SystemPrompt    string         `json:"systemPrompt,omitempty"`
	PolicyMode      string         `json:"policyMode,omitempty"`
	ExtraReadRoots  []string       `json:"extraReadRoots,omitempty"`
	ExtraWriteRoots []string       `json:"extraWriteRoots,omitempty"`
}

// Resolver normalizes a local plugin root into runtime contributions.
type Resolver interface {
	Resolve(context.Context, ResolveRequest) (Resolved, error)
}

// Store persists installed plugin records.
type Store interface {
	Install(context.Context, Resolved) (Installed, error)
	List(context.Context) ([]Installed, error)
	Load(context.Context, string) (Installed, error)
}

// Installer resolves and records plugin installations.
type Installer interface {
	Install(context.Context, InstallRequest) (Installed, error)
}

// Registry resolves installed plugins.
type Registry interface {
	List(context.Context) ([]Resolved, error)
	Load(context.Context, string) (Resolved, error)
}
