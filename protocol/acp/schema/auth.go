package schema

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

const (
	AuthMethodTypeAgent    = "agent"
	AuthMethodTypeTerminal = "terminal"
)

// DecodeAuthMethods normalizes valid v1 authentication descriptors while
// skipping malformed entries, matching ACP's tolerant initialize decoding.
func DecodeAuthMethods(rawMethods []json.RawMessage) []AuthMethod {
	out := make([]AuthMethod, 0, len(rawMethods))
	seen := make(map[string]struct{}, len(rawMethods))
	for _, raw := range rawMethods {
		method, err := DecodeAuthMethod(raw)
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

// DecodeAuthMethod decodes one stable v1 agent method or Preview terminal
// method. Missing type is the stable agent-managed flow.
func DecodeAuthMethod(raw json.RawMessage) (AuthMethod, error) {
	var method AuthMethod
	if err := json.Unmarshal(raw, &method); err != nil {
		return AuthMethod{}, fmt.Errorf("acp/schema: decode auth method: %w", err)
	}
	method.ID = strings.TrimSpace(method.ID)
	method.Name = strings.TrimSpace(method.Name)
	method.Description = strings.TrimSpace(method.Description)
	method.Type = strings.ToLower(strings.TrimSpace(method.Type))
	if method.Type == "" {
		method.Type = AuthMethodTypeAgent
	}
	if method.ID == "" || method.Name == "" {
		return AuthMethod{}, fmt.Errorf("acp/schema: auth method id and name are required")
	}
	switch method.Type {
	case AuthMethodTypeAgent:
		method.Args = nil
		method.Env = nil
	case AuthMethodTypeTerminal:
		method.Args = append([]string(nil), method.Args...)
		method.Env = maps.Clone(method.Env)
	default:
		return AuthMethod{}, fmt.Errorf("acp/schema: unsupported auth method type %q", method.Type)
	}
	return method, nil
}
