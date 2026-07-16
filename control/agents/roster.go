package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// ModelBackingSelection describes one already configured built-in model that
// should have a stable user-facing Agent identity.
type ModelBackingSelection struct {
	Alias     string
	Name      string
	Namespace string
}

// UpsertExternalAgent adds or updates exactly one model-backed identity for an
// external ACP connection. Other Agents on the same connection are preserved.
func UpsertExternalAgent(
	current Configuration,
	connection Connection,
	model RemoteModel,
	defaults SessionOptions,
	snapshot DiscoverySnapshot,
	allowed NameFilter,
) (Configuration, Agent, error) {
	current = NormalizeConfiguration(current)
	connection = NormalizeConnection(connection)
	if err := ValidateConnection(connection); err != nil {
		return Configuration{}, Agent{}, err
	}
	model = RemoteModel{
		ID:          strings.TrimSpace(model.ID),
		Name:        strings.TrimSpace(model.Name),
		Description: strings.TrimSpace(model.Description),
	}
	if model.ID == "" {
		return Configuration{}, Agent{}, fmt.Errorf("control/agents: external model id is required")
	}
	defaults = NormalizeSessionOptions(defaults)
	if defaults.ModelID == "" {
		defaults.ModelID = model.ID
	}
	if defaults.ModelID != model.ID {
		return Configuration{}, Agent{}, fmt.Errorf("control/agents: selected model %q does not match defaults model %q", model.ID, defaults.ModelID)
	}
	if previous, ok := LookupConnection(current, connection.ID); ok && LaunchFingerprint(previous.Launcher) != LaunchFingerprint(connection.Launcher) {
		for _, agent := range current.Agents {
			if agent.Backing.ConnectionID == connection.ID && agent.Defaults.ModelID != model.ID {
				return Configuration{}, Agent{}, fmt.Errorf(
					"control/agents: cannot change launcher for connection %q while sibling Agent %q uses it",
					connection.ID,
					agent.ID,
				)
			}
		}
	}

	next := Configuration{}
	used := map[string]struct{}{}
	var existing Agent
	for _, agent := range current.Agents {
		if agent.Backing.ConnectionID == connection.ID && agent.Defaults.ModelID == model.ID {
			existing = agent
			continue
		}
		next.Agents = append(next.Agents, agent)
		used[agent.ID] = struct{}{}
	}
	id := existing.ID
	providerID := Slug(firstNonEmpty(connection.Name, connection.ID))
	if !externalAgentIDMatchesProvider(id, providerID) || !agentIDAvailable(id, used, allowed) {
		id = allocateExternalAgentID(connection, model, used, allowed)
	}
	if id == "" {
		return Configuration{}, Agent{}, fmt.Errorf("control/agents: cannot allocate an addressable Agent id for model %q", model.ID)
	}
	agent := NormalizeAgent(Agent{
		ID:       id,
		Name:     externalAgentDisplayName(connection, model),
		Backing:  AgentBacking{ConnectionID: connection.ID},
		Defaults: defaults,
	})
	next.Agents = append(next.Agents, agent)

	for _, item := range current.Connections {
		if item.ID != connection.ID {
			next.Connections = append(next.Connections, item)
		}
	}
	next.Connections = append(next.Connections, connection)

	snapshot = NormalizeDiscoverySnapshot(snapshot)
	snapshot.ConnectionID = connection.ID
	snapshot.LaunchFingerprint = LaunchFingerprint(connection.Launcher)
	if snapshot.SelectedModelID == "" {
		snapshot.SelectedModelID = model.ID
	}
	for _, item := range current.Discoveries {
		if item.ConnectionID != connection.ID {
			next.Discoveries = append(next.Discoveries, item)
			continue
		}
		if item.LaunchFingerprint != snapshot.LaunchFingerprint {
			continue
		}
		if item.CWD == snapshot.CWD && item.SelectedModelID == snapshot.SelectedModelID {
			continue
		}
		next.Discoveries = append(next.Discoveries, item)
	}
	next.Discoveries = append(next.Discoveries, snapshot)
	next = NormalizeConfiguration(next)
	if err := ValidateConfiguration(next); err != nil {
		return Configuration{}, Agent{}, err
	}
	return next, agent, nil
}

func allocateExternalAgentID(connection Connection, model RemoteModel, used map[string]struct{}, allowed NameFilter) string {
	provider := Slug(firstNonEmpty(connection.Name, connection.ID))
	if provider == "" || startsWithDigit(provider) {
		provider = "agent"
	}
	modelID := Slug(firstNonEmpty(model.Name, model.ID))
	if modelID == "" {
		modelID = "model"
	}
	candidates := []string{
		provider,
		provider + "-" + modelID,
		provider + "-" + modelID + "-x" + shortDigest(connection.ID+"\x00"+model.ID),
	}
	for _, candidate := range candidates {
		if agentIDAvailable(candidate, used, allowed) {
			return candidate
		}
	}
	stable := candidates[len(candidates)-1]
	for suffix := 2; suffix < 10_000; suffix++ {
		candidate := fmt.Sprintf("%s-alt%d", stable, suffix)
		if agentIDAvailable(candidate, used, allowed) {
			return candidate
		}
	}
	return ""
}

func externalAgentIDMatchesProvider(id string, provider string) bool {
	id = NormalizeName(id)
	provider = Slug(provider)
	return id != "" && provider != "" && (id == provider || strings.HasPrefix(id, provider+"-"))
}

func externalAgentDisplayName(connection Connection, model RemoteModel) string {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(connection.Name, connection.ID, "agent")))
	modelName := displayQualifier(firstNonEmpty(model.Name, model.ID))
	if modelName == "" {
		return provider
	}
	return provider + "(" + modelName + ")"
}

func displayQualifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	separator := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			out.WriteRune(r)
			separator = false
		case out.Len() > 0 && !separator:
			out.WriteByte('-')
			separator = true
		}
	}
	return strings.Trim(out.String(), "-_")
}

// UpsertModelBackedAgents adds or updates stable Agent identities for already
// configured built-in model aliases while preserving every unrelated Agent.
func UpsertModelBackedAgents(current Configuration, selections []ModelBackingSelection, allowed NameFilter) (Configuration, []Agent, error) {
	current = NormalizeConfiguration(current)
	selected := make(map[string]ModelBackingSelection, len(selections))
	order := make([]string, 0, len(selections))
	for _, selection := range selections {
		selection.Alias = strings.ToLower(strings.TrimSpace(selection.Alias))
		selection.Name = strings.TrimSpace(selection.Name)
		selection.Namespace = strings.TrimSpace(selection.Namespace)
		if selection.Alias == "" {
			return Configuration{}, nil, fmt.Errorf("control/agents: configured model alias is required")
		}
		if _, exists := selected[selection.Alias]; !exists {
			order = append(order, selection.Alias)
		}
		selected[selection.Alias] = selection
	}

	next := Configuration{
		Connections: append([]Connection(nil), current.Connections...),
		Discoveries: append([]DiscoverySnapshot(nil), current.Discoveries...),
	}
	existing := map[string]Agent{}
	used := map[string]struct{}{}
	for _, agent := range current.Agents {
		alias := strings.ToLower(strings.TrimSpace(agent.Backing.ModelAlias))
		if _, ok := selected[alias]; ok {
			existing[alias] = agent
			continue
		}
		next.Agents = append(next.Agents, agent)
		used[agent.ID] = struct{}{}
	}

	out := make([]Agent, 0, len(order))
	for _, alias := range order {
		selection := selected[alias]
		id := existing[alias].ID
		if !agentIDAvailable(id, used, allowed) {
			id = allocateAgentID(firstNonEmpty(selection.Name, alias), selection.Namespace, "model\x00"+alias, used, allowed)
		}
		if id == "" {
			return Configuration{}, nil, fmt.Errorf("control/agents: cannot allocate an addressable Agent id for model alias %q", alias)
		}
		agent := NormalizeAgent(Agent{
			ID:      id,
			Name:    firstNonEmpty(selection.Name, alias),
			Backing: AgentBacking{ModelAlias: alias},
		})
		next.Agents = append(next.Agents, agent)
		out = append(out, agent)
		used[id] = struct{}{}
	}
	next = NormalizeConfiguration(next)
	if err := ValidateConfiguration(next); err != nil {
		return Configuration{}, nil, err
	}
	return next, out, nil
}

// RemoveModelBackedAgent removes the Agent backed by modelAlias.
func RemoveModelBackedAgent(current Configuration, modelAlias string) Configuration {
	current = NormalizeConfiguration(current)
	modelAlias = strings.ToLower(strings.TrimSpace(modelAlias))
	next := Configuration{
		Connections: append([]Connection(nil), current.Connections...),
		Discoveries: append([]DiscoverySnapshot(nil), current.Discoveries...),
	}
	for _, agent := range current.Agents {
		if strings.EqualFold(agent.Backing.ModelAlias, modelAlias) {
			continue
		}
		next.Agents = append(next.Agents, agent)
	}
	return NormalizeConfiguration(next)
}

// CustomConnectionID returns a stable connection identity derived from the
// complete launcher rather than only the executable basename.
func CustomConnectionID(command string, launcher Launcher) string {
	base := Slug(filepath.Base(strings.TrimSpace(command)))
	if base == "" {
		base = "agent"
	}
	return "custom-" + base + "-x" + shortDigest(LaunchFingerprint(launcher))
}

// Slug normalizes a display value into the Agent name alphabet.
func Slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	dash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			dash = false
		case out.Len() > 0 && !dash:
			out.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

func allocateAgentID(preferred string, namespace string, backingKey string, used map[string]struct{}, allowed NameFilter) string {
	namespace = Slug(namespace)
	if namespace == "" || startsWithDigit(namespace) {
		namespace = "agent"
	}
	base := Slug(preferred)
	if base == "" {
		base = namespace + "-agent"
	}
	if startsWithDigit(base) {
		base = namespace + "-" + base
	}
	candidates := []string{
		base,
		namespace + "-" + base,
		namespace + "-" + base + "-x" + shortDigest(backingKey),
	}
	for _, candidate := range candidates {
		if agentIDAvailable(candidate, used, allowed) {
			return candidate
		}
	}
	stable := candidates[len(candidates)-1]
	for suffix := 2; suffix < 10_000; suffix++ {
		candidate := fmt.Sprintf("%s-alt%d", stable, suffix)
		if agentIDAvailable(candidate, used, allowed) {
			return candidate
		}
	}
	return ""
}

func agentIDAvailable(id string, used map[string]struct{}, allowed NameFilter) bool {
	id = NormalizeName(id)
	if !IsName(id) {
		return false
	}
	if _, exists := used[id]; exists {
		return false
	}
	return allowed == nil || allowed(id)
}

func startsWithDigit(value string) bool {
	return value != "" && value[0] >= '0' && value[0] <= '9'
}

func shortDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
