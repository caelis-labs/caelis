package agents

import (
	"strings"
)

// NormalizeName returns the canonical user-addressable Agent name.
func NormalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(strings.TrimPrefix(name, "/"), "@")
	return strings.ToLower(strings.TrimSpace(name))
}

// IsName reports whether name has the canonical Agent slug shape. Product
// command reservations are applied by the command registry that consumes it.
func IsName(name string) bool {
	name = NormalizeName(name)
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		case i > 0 && (r == '-' || r == '_'):
		default:
			return false
		}
	}
	return true
}

// FormatRunName returns the canonical <agent>(<handle>) direct Agent run name.
// The stable Agent identifies the global roster entry while the human handle
// identifies exactly one participant in the current Session.
func FormatRunName(agent string, handle string) string {
	agent = NormalizeName(agent)
	handle = normalizeRunHandle(handle)
	if !IsName(agent) || !isRunHandle(handle) {
		return ""
	}
	return agent + "(" + handle + ")"
}

// ParseRunName parses the canonical <agent>(<handle>) direct Agent run name.
func ParseRunName(name string) (agent string, handle string, ok bool) {
	name = NormalizeName(name)
	open := strings.LastIndexByte(name, '(')
	if open <= 0 || !strings.HasSuffix(name, ")") || open+2 >= len(name) {
		return "", "", false
	}
	agent = NormalizeName(name[:open])
	handle = normalizeRunHandle(name[open+1 : len(name)-1])
	if !IsName(agent) || !isRunHandle(handle) {
		return "", "", false
	}
	if FormatRunName(agent, handle) != name {
		return "", "", false
	}
	return agent, handle, true
}

func normalizeRunHandle(handle string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
}

func isRunHandle(handle string) bool {
	handle = normalizeRunHandle(handle)
	if handle == "" {
		return false
	}
	for i, r := range handle {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		case i > 0 && (r == '-' || r == '_'):
		default:
			return false
		}
	}
	return true
}

// Run is the neutral Control view of one direct Agent run that a surface may
// address. Delivery-specific participant kinds and roles are normalized before
// constructing this value.
type Run struct {
	Name        string
	Agent       string
	Addressable bool
}

// NameFilter decides whether a normalized Agent name is eligible for a
// product surface, including command-name reservations.
type NameFilter func(string) bool

// AppendRunNames appends qualified run names and unique bare handles, preserving
// order and existing command names. Callers give core and configured role
// commands precedence over bare handles.
func AppendRunNames(base []string, runs []Run, filters ...NameFilter) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]struct{}, len(out))
	for _, name := range out {
		seen[NormalizeName(name)] = struct{}{}
	}
	// Count before applying surface filters: filtering out one of two matching
	// handles must not silently change which participant receives the input.
	valid := make([]string, 0, len(runs))
	handles := make(map[string]int)
	qualified := make(map[string]bool)
	for _, run := range runs {
		if !run.Addressable {
			continue
		}
		name := NormalizeName(run.Name)
		agent, handle, ok := ParseRunName(name)
		if !ok {
			continue
		}
		if configured := NormalizeName(run.Agent); configured != "" && configured != agent {
			continue
		}
		if !qualified[name] {
			qualified[name] = true
			valid = append(valid, name)
			handles[handle]++
		}
	}
	for _, name := range valid {
		agent, handle, _ := ParseRunName(name)
		if !nameAllowed(agent, filters...) {
			continue
		}
		candidates := []string{name}
		if handles[handle] == 1 {
			candidates = append(candidates, handle)
		}
		for _, candidate := range candidates {
			if _, exists := seen[candidate]; !exists {
				out = append(out, candidate)
				seen[candidate] = struct{}{}
			}
		}
	}
	return out
}

// RunNameAllowed reports whether command identifies one addressable direct
// Agent run.
func RunNameAllowed(runs []Run, command string, filters ...NameFilter) bool {
	command = NormalizeName(command)
	for _, name := range AppendRunNames(nil, runs, filters...) {
		if name == command {
			return true
		}
	}
	return false
}

func nameAllowed(name string, filters ...NameFilter) bool {
	for _, filter := range filters {
		if filter != nil && !filter(name) {
			return false
		}
	}
	return true
}
