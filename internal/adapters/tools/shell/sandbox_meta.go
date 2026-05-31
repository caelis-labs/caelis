package shell

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
)

func addSandboxExecutionMeta(meta map[string]any, runtime sandbox.Runtime, constraints sandbox.Constraints, result sandbox.CommandResult) {
	if meta == nil || runtime == nil {
		return
	}
	policy := sandboxExecutionMeta(runtime.Descriptor(), constraints, result)
	if len(policy) == 0 {
		return
	}
	meta["sandbox"] = policy
	runtimeMeta := nestedMap(meta, "caelis", "runtime")
	runtimeMeta["sandbox"] = policy
	if diagnostics, ok := policy["diagnostics"]; ok {
		meta["sandbox_diagnostics"] = diagnostics
	}
}

func sandboxExecutionMeta(desc sandbox.Descriptor, constraints sandbox.Constraints, result sandbox.CommandResult) map[string]any {
	constraints = sandbox.NormalizeConstraints(constraints)
	backend := firstNonEmptyString(string(result.Backend), string(constraints.Backend), string(desc.DefaultConstraints.Backend), string(desc.Backend))
	route := firstNonEmptyString(string(constraints.Route), string(result.Route), string(desc.DefaultConstraints.Route), routeForBackend(backend))
	policy := map[string]any{
		"route":           strings.TrimSpace(route),
		"backend":         strings.TrimSpace(backend),
		"permission":      firstNonEmptyString(string(constraints.Permission), string(desc.DefaultConstraints.Permission)),
		"isolation":       firstNonEmptyString(string(constraints.Isolation), string(desc.DefaultConstraints.Isolation), string(desc.Isolation)),
		"network":         firstNonEmptyString(string(constraints.Network), string(desc.DefaultConstraints.Network)),
		"network_control": desc.Capabilities.NetworkControl,
		"path_policy":     desc.Capabilities.PathPolicy,
		"path_rules":      len(constraints.PathRules),
	}
	if diagnostics := sandboxExecutionDiagnostics(policy); len(diagnostics) > 0 {
		policy["diagnostics"] = diagnostics
	}
	return compactAnyMap(policy)
}

func sandboxExecutionDiagnostics(policy map[string]any) []map[string]string {
	var out []map[string]string
	route, _ := policy["route"].(string)
	if strings.EqualFold(strings.TrimSpace(route), string(sandbox.RouteHost)) {
		out = append(out, map[string]string{
			"severity": "warning",
			"kind":     "route",
			"message":  "command executed on the host route",
		})
	}
	network, _ := policy["network"].(string)
	networkControl, _ := policy["network_control"].(bool)
	if strings.TrimSpace(network) != "" && !strings.EqualFold(strings.TrimSpace(network), string(sandbox.NetworkInherit)) && !networkControl {
		out = append(out, map[string]string{
			"severity": "warning",
			"kind":     "network",
			"message":  "command requested network policy but backend does not report network control",
		})
	}
	pathRules, _ := policy["path_rules"].(int)
	pathPolicy, _ := policy["path_policy"].(bool)
	if pathRules > 0 && !pathPolicy {
		out = append(out, map[string]string{
			"severity": "warning",
			"kind":     "roots",
			"message":  "command requested path rules but backend does not report path policy enforcement",
		})
	}
	return out
}

func nestedMap(root map[string]any, keys ...string) map[string]any {
	current := root
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		next, _ := current[key].(map[string]any)
		if next == nil {
			next = map[string]any{}
			current[key] = next
		}
		current = next
	}
	return current
}

func compactAnyMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) == "" {
				continue
			}
			out[key] = strings.TrimSpace(typed)
		case int:
			if typed == 0 {
				continue
			}
			out[key] = typed
		default:
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func routeForBackend(backend string) string {
	if strings.EqualFold(strings.TrimSpace(backend), string(sandbox.BackendHost)) {
		return string(sandbox.RouteHost)
	}
	if strings.TrimSpace(backend) == "" {
		return ""
	}
	return string(sandbox.RouteSandbox)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}
