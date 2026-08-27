package authentication

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

const (
	authMethodTypeAgent    = "agent"
	authMethodTypeTerminal = "terminal"
)

type wireAuthMethod struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

func decodeAuthMethods(rawMethods []json.RawMessage) []wireAuthMethod {
	out := make([]wireAuthMethod, 0, len(rawMethods))
	seen := make(map[string]struct{}, len(rawMethods))
	for _, raw := range rawMethods {
		method, err := decodeAuthMethod(raw)
		if err != nil {
			continue
		}
		if _, exists := seen[method.ID]; exists {
			continue
		}
		seen[method.ID] = struct{}{}
		out = append(out, method)
	}
	return out
}

func decodeAuthMethod(raw json.RawMessage) (wireAuthMethod, error) {
	var method wireAuthMethod
	if err := json.Unmarshal(raw, &method); err != nil {
		return wireAuthMethod{}, fmt.Errorf("internal/acpagentbridge/authentication: decode auth method: %w", err)
	}
	method.ID = strings.TrimSpace(method.ID)
	method.Name = strings.TrimSpace(method.Name)
	method.Description = strings.TrimSpace(method.Description)
	method.Type = strings.ToLower(strings.TrimSpace(method.Type))
	if method.Type == "" {
		method.Type = authMethodTypeAgent
	}
	if method.ID == "" || method.Name == "" {
		return wireAuthMethod{}, fmt.Errorf("internal/acpagentbridge/authentication: auth method id and name are required")
	}
	switch method.Type {
	case authMethodTypeAgent:
		method.Args = nil
		method.Env = nil
	case authMethodTypeTerminal:
		method.Args = append([]string(nil), method.Args...)
		method.Env = maps.Clone(method.Env)
	default:
		return wireAuthMethod{}, fmt.Errorf(
			"internal/acpagentbridge/authentication: unsupported auth method type %q",
			method.Type,
		)
	}
	return method, nil
}

func methodFromWire(method wireAuthMethod) controlagents.AuthenticationMethod {
	return controlagents.NormalizeAuthenticationMethod(controlagents.AuthenticationMethod{
		ID:          method.ID,
		Name:        method.Name,
		Description: method.Description,
		Type:        controlagents.AuthenticationType(method.Type),
		Args:        append([]string(nil), method.Args...),
		Env:         maps.Clone(method.Env),
	})
}
