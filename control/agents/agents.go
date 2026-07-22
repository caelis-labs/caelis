// Package agents defines Control-owned external ACP Agent identities,
// connections, discovery data, and concrete runtime session options.
package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"
)

// LaunchKind describes how an external ACP process is launched.
type LaunchKind string

const (
	// LaunchKindExecutable invokes an installed executable directly.
	LaunchKindExecutable LaunchKind = "executable"
	// LaunchKindPackageExec invokes a package runner such as npx.
	LaunchKindPackageExec LaunchKind = "package_exec"
	// LaunchKindManaged invokes an executable installed into Caelis-managed storage.
	LaunchKindManaged LaunchKind = "managed"
)

// Launcher is the complete process declaration for one external ACP endpoint.
// Model selection deliberately does not belong in this type; standard
// ModelProfiles own remote model and default selection.
type Launcher struct {
	Kind    LaunchKind        `json:"kind,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	WorkDir string            `json:"work_dir,omitempty"`
}

// Connection is one reusable external ACP endpoint.
type Connection struct {
	ID       string   `json:"id,omitempty"`
	Name     string   `json:"name,omitempty"`
	Launcher Launcher `json:"launcher,omitempty"`
}

// SessionOptions are concrete values applied after ACP session creation or
// resume and before the first prompt. ModelID is applied first, non-effort
// ConfigValues next, and ReasoningEffortConfigID last.
type SessionOptions struct {
	ModelID                 string            `json:"model_id,omitempty"`
	ConfigValues            map[string]string `json:"config_values,omitempty"`
	ReasoningEffortConfigID string            `json:"reasoning_effort_config_id,omitempty"`
}

// Agent is one stable external connection identity. It contains no scenario
// prompt or model selection; Control supplies both through a frozen Placement.
type Agent struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
}

// ConfigChoice is one value advertised for a session config option.
type ConfigChoice struct {
	Value       string `json:"value,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ConfigOptionPurpose is the normalized semantic role of an ACP session
// option. Discovery classifies protocol vocabulary once so presentation code
// does not repeat vendor-name heuristics.
type ConfigOptionPurpose string

const (
	ConfigOptionPurposeReasoningEffort ConfigOptionPurpose = "reasoning_effort"
)

// ConfigOption is the persisted discovery view of one ACP session option.
type ConfigOption struct {
	ID           string              `json:"id,omitempty"`
	Name         string              `json:"name,omitempty"`
	Type         string              `json:"type,omitempty"`
	Category     string              `json:"category,omitempty"`
	Description  string              `json:"description,omitempty"`
	CurrentValue string              `json:"current_value,omitempty"`
	Purpose      ConfigOptionPurpose `json:"purpose,omitempty"`
	Options      []ConfigChoice      `json:"options,omitempty"`
}

// RemoteModel is one model advertised by an ACP session.
type RemoteModel struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ModelControlKind records how a discovery session advertised model changes.
// It is an observation for display and diagnostics; runtime application always
// re-detects the mechanism from the real session handshake.
type ModelControlKind string

const (
	ModelControlConfigOption ModelControlKind = "config_option"
	ModelControlSetModel     ModelControlKind = "set_model"
)

// ModelControl describes the model-selection mechanism observed by discovery.
type ModelControl struct {
	Kind     ModelControlKind `json:"kind,omitempty"`
	ConfigID string           `json:"config_id,omitempty"`
}

// DiscoverySnapshot is a cache of one session-scoped ACP model discovery.
// It is never authoritative for later sessions; runtime must validate the
// selected defaults against each real session handshake.
type DiscoverySnapshot struct {
	ConnectionID      string         `json:"connection_id,omitempty"`
	LaunchFingerprint string         `json:"launch_fingerprint,omitempty"`
	CWD               string         `json:"cwd,omitempty"`
	ProtocolVersion   int            `json:"protocol_version,omitempty"`
	SelectedModelID   string         `json:"selected_model_id,omitempty"`
	CurrentModelID    string         `json:"current_model_id,omitempty"`
	Models            []RemoteModel  `json:"models,omitempty"`
	ConfigOptions     []ConfigOption `json:"config_options,omitempty"`
	ModelControl      ModelControl   `json:"model_control,omitempty"`
	DiscoveredAt      time.Time      `json:"discovered_at,omitempty"`
}

// Configuration is the persisted Control-owned Agent roster.
type Configuration struct {
	Connections []Connection        `json:"connections,omitempty"`
	Agents      []Agent             `json:"agents,omitempty"`
	Discoveries []DiscoverySnapshot `json:"discoveries,omitempty"`
}

// ListAgents returns the single detached, deterministic Agent roster consumed
// by user dispatch, Agent Spawn, and Control-authorized handoff resolution.
func ListAgents(in Configuration) []Agent {
	cfg := NormalizeConfiguration(in)
	out := make([]Agent, 0, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		out = append(out, NormalizeAgent(agent))
	}
	return out
}

// LookupAgent resolves one stable Agent identity.
func LookupAgent(in Configuration, id string) (Agent, bool) {
	id = normalizeID(id)
	for _, agent := range NormalizeConfiguration(in).Agents {
		if agent.ID == id {
			return NormalizeAgent(agent), true
		}
	}
	return Agent{}, false
}

// LookupConnection resolves one external ACP connection.
func LookupConnection(in Configuration, id string) (Connection, bool) {
	id = normalizeID(id)
	for _, connection := range NormalizeConfiguration(in).Connections {
		if connection.ID == id {
			return NormalizeConnection(connection), true
		}
	}
	return Connection{}, false
}

// ResolveAgent returns one Agent together with its external ACP connection.
func ResolveAgent(in Configuration, id string) (Agent, Connection, error) {
	agent, ok := LookupAgent(in, id)
	if !ok {
		return Agent{}, Connection{}, fmt.Errorf("control/agents: agent %q not found", strings.TrimSpace(id))
	}
	connection, ok := LookupConnection(in, agent.ConnectionID)
	if !ok {
		return Agent{}, Connection{}, fmt.Errorf("control/agents: agent %q references unknown connection %q", agent.ID, agent.ConnectionID)
	}
	return agent, connection, nil
}

// NormalizeLauncher returns a detached canonical launcher value.
func NormalizeLauncher(in Launcher) Launcher {
	out := Launcher{
		Kind:    LaunchKind(strings.ToLower(strings.TrimSpace(string(in.Kind)))),
		Command: strings.TrimSpace(in.Command),
		WorkDir: strings.TrimSpace(in.WorkDir),
		Env:     maps.Clone(in.Env),
	}
	if len(in.Args) > 0 {
		out.Args = append([]string(nil), in.Args...)
	}
	if out.Kind == "" {
		out.Kind = LaunchKindExecutable
	}
	return out
}

// ValidateConnection checks the durable connection contract.
func ValidateConnection(in Connection) error {
	connection := NormalizeConnection(in)
	if connection.ID == "" {
		return fmt.Errorf("control/agents: connection id is required")
	}
	if connection.Launcher.Command == "" {
		return fmt.Errorf("control/agents: connection %q command is required", connection.ID)
	}
	switch connection.Launcher.Kind {
	case LaunchKindExecutable, LaunchKindPackageExec, LaunchKindManaged:
		return nil
	default:
		return fmt.Errorf("control/agents: connection %q has unsupported launch kind %q", connection.ID, connection.Launcher.Kind)
	}
}

// NormalizeConnection returns a detached canonical connection.
func NormalizeConnection(in Connection) Connection {
	out := Connection{
		ID:       normalizeID(in.ID),
		Name:     strings.TrimSpace(in.Name),
		Launcher: NormalizeLauncher(in.Launcher),
	}
	if out.Name == "" {
		out.Name = out.ID
	}
	return out
}

// NormalizeSessionOptions returns detached desired session defaults.
func NormalizeSessionOptions(in SessionOptions) SessionOptions {
	out := SessionOptions{
		ModelID:                 strings.TrimSpace(in.ModelID),
		ReasoningEffortConfigID: strings.TrimSpace(in.ReasoningEffortConfigID),
	}
	for key, value := range in.ConfigValues {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if out.ConfigValues == nil {
			out.ConfigValues = map[string]string{}
		}
		out.ConfigValues[key] = value
	}
	return out
}

// NormalizeAgent returns a detached canonical Agent.
func NormalizeAgent(in Agent) Agent {
	out := Agent{
		ID:           NormalizeName(in.ID),
		Name:         strings.TrimSpace(in.Name),
		ConnectionID: normalizeID(in.ConnectionID),
	}
	if out.Name == "" {
		out.Name = out.ID
	}
	return out
}

// ValidateAgent checks one Agent against a connection catalog.
func ValidateAgent(in Agent, connections []Connection) error {
	agent := NormalizeAgent(in)
	if agent.ID == "" {
		return fmt.Errorf("control/agents: agent id is required")
	}
	if !IsName(agent.ID) {
		return fmt.Errorf("control/agents: agent id %q is not a valid addressable name", agent.ID)
	}
	if agent.ConnectionID == "" {
		return fmt.Errorf("control/agents: agent %q requires an external ACP connection", agent.ID)
	}
	for _, connection := range connections {
		if NormalizeConnection(connection).ID == agent.ConnectionID {
			return nil
		}
	}
	return fmt.Errorf("control/agents: agent %q references unknown connection %q", agent.ID, agent.ConnectionID)
}

// LaunchFingerprint identifies changes that invalidate cached discovery data.
func LaunchFingerprint(in Launcher) string {
	launcher := NormalizeLauncher(in)
	payload, _ := json.Marshal(launcher)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// NormalizeConfiguration returns a detached, deterministically ordered roster.
func NormalizeConfiguration(in Configuration) Configuration {
	out := Configuration{}
	seenConnections := map[string]struct{}{}
	for _, raw := range in.Connections {
		connection := NormalizeConnection(raw)
		if connection.ID == "" {
			continue
		}
		if _, ok := seenConnections[connection.ID]; ok {
			continue
		}
		seenConnections[connection.ID] = struct{}{}
		out.Connections = append(out.Connections, connection)
	}
	seenAgents := map[string]struct{}{}
	for _, raw := range in.Agents {
		agent := NormalizeAgent(raw)
		if agent.ID == "" {
			continue
		}
		if _, ok := seenAgents[agent.ID]; ok {
			continue
		}
		seenAgents[agent.ID] = struct{}{}
		out.Agents = append(out.Agents, agent)
	}
	seenDiscoveries := map[string]struct{}{}
	for _, raw := range in.Discoveries {
		snapshot := NormalizeDiscoverySnapshot(raw)
		if snapshot.ConnectionID == "" {
			continue
		}
		key := snapshot.ConnectionID + "\x00" + snapshot.LaunchFingerprint + "\x00" + snapshot.CWD + "\x00" + snapshot.SelectedModelID
		if _, ok := seenDiscoveries[key]; ok {
			continue
		}
		seenDiscoveries[key] = struct{}{}
		out.Discoveries = append(out.Discoveries, snapshot)
	}
	sort.Slice(out.Connections, func(i, j int) bool { return out.Connections[i].ID < out.Connections[j].ID })
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].ID < out.Agents[j].ID })
	sort.Slice(out.Discoveries, func(i, j int) bool {
		if out.Discoveries[i].ConnectionID != out.Discoveries[j].ConnectionID {
			return out.Discoveries[i].ConnectionID < out.Discoveries[j].ConnectionID
		}
		return out.Discoveries[i].CWD < out.Discoveries[j].CWD
	})
	return out
}

// ValidateConfiguration validates all persisted roster references.
func ValidateConfiguration(in Configuration) error {
	connections := make([]Connection, 0, len(in.Connections))
	connectionIDs := map[string]struct{}{}
	for _, raw := range in.Connections {
		connection := NormalizeConnection(raw)
		if err := ValidateConnection(connection); err != nil {
			return err
		}
		if _, exists := connectionIDs[connection.ID]; exists {
			return fmt.Errorf("control/agents: duplicate connection id %q", connection.ID)
		}
		connectionIDs[connection.ID] = struct{}{}
		connections = append(connections, connection)
	}
	agentIDs := map[string]struct{}{}
	for _, raw := range in.Agents {
		agent := NormalizeAgent(raw)
		if err := ValidateAgent(agent, connections); err != nil {
			return err
		}
		if _, exists := agentIDs[agent.ID]; exists {
			return fmt.Errorf("control/agents: duplicate agent id %q", agent.ID)
		}
		agentIDs[agent.ID] = struct{}{}
	}
	return nil
}

// NormalizeDiscoverySnapshot returns a detached canonical cache entry.
func NormalizeDiscoverySnapshot(in DiscoverySnapshot) DiscoverySnapshot {
	out := DiscoverySnapshot{
		ConnectionID:      normalizeID(in.ConnectionID),
		LaunchFingerprint: strings.TrimSpace(in.LaunchFingerprint),
		CWD:               strings.TrimSpace(in.CWD),
		ProtocolVersion:   in.ProtocolVersion,
		SelectedModelID:   strings.TrimSpace(in.SelectedModelID),
		CurrentModelID:    strings.TrimSpace(in.CurrentModelID),
		ModelControl: ModelControl{
			Kind:     ModelControlKind(strings.ToLower(strings.TrimSpace(string(in.ModelControl.Kind)))),
			ConfigID: strings.TrimSpace(in.ModelControl.ConfigID),
		},
		DiscoveredAt: in.DiscoveredAt,
	}
	seenModels := map[string]struct{}{}
	for _, model := range in.Models {
		model = RemoteModel{ID: strings.TrimSpace(model.ID), Name: strings.TrimSpace(model.Name), Description: strings.TrimSpace(model.Description)}
		if model.ID == "" {
			continue
		}
		if _, ok := seenModels[model.ID]; ok {
			continue
		}
		seenModels[model.ID] = struct{}{}
		out.Models = append(out.Models, model)
	}
	for _, option := range in.ConfigOptions {
		option.ID = strings.TrimSpace(option.ID)
		if option.ID == "" {
			continue
		}
		option.Name = strings.TrimSpace(option.Name)
		option.Type = strings.TrimSpace(option.Type)
		option.Category = strings.TrimSpace(option.Category)
		option.Description = strings.TrimSpace(option.Description)
		option.CurrentValue = strings.TrimSpace(option.CurrentValue)
		option.Purpose = ConfigOptionPurpose(strings.ToLower(strings.TrimSpace(string(option.Purpose))))
		if option.Purpose == "" {
			option.Purpose = classifyConfigOptionPurpose(option)
		}
		option.Options = append([]ConfigChoice(nil), option.Options...)
		out.ConfigOptions = append(out.ConfigOptions, option)
	}
	return out
}

func classifyConfigOptionPurpose(option ConfigOption) ConfigOptionPurpose {
	compact := func(value string) string {
		return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(value)))
	}
	id := compact(option.ID)
	switch id {
	case "effort", "reasoningeffort", "reasoninglevel", "reasoningdepth", "thoughtlevel", "thoughtdepth", "thinkinglevel", "thinkingdepth":
		return ConfigOptionPurposeReasoningEffort
	}
	category := compact(option.Category)
	if category != "thoughtlevel" && category != "reasoning" && category != "reasoningeffort" {
		return ""
	}
	name := compact(option.Name)
	if strings.Contains(id, "effort") || strings.Contains(id, "level") || strings.Contains(id, "depth") ||
		strings.Contains(name, "effort") || strings.Contains(name, "level") || strings.Contains(name, "depth") {
		return ConfigOptionPurposeReasoningEffort
	}
	return ""
}

func normalizeID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
