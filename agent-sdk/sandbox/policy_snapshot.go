package sandbox

import "strings"

// PolicySnapshot is the complete effective default execution boundary pinned
// by a Runtime. It contains facts, not authorization.
type PolicySnapshot struct {
	Route            Route      `json:"route,omitempty"`
	Backend          Backend    `json:"backend,omitempty"`
	Permission       Permission `json:"permission,omitempty"`
	Isolation        Isolation  `json:"isolation,omitempty"`
	Network          Network    `json:"network,omitempty"`
	WritableRoots    []string   `json:"writable_roots,omitempty"`
	ReadOnlySubpaths []string   `json:"read_only_subpaths,omitempty"`
}

// PolicySummary is the compact, actionable boundary exposed to models.
// Permission describes workspace access; backend details and enumerated
// writable roots remain Runtime implementation facts.
type PolicySummary struct {
	Route            Route      `json:"route,omitempty"`
	Permission       Permission `json:"permission,omitempty"`
	Network          Network    `json:"network,omitempty"`
	ReadOnlySubpaths []string   `json:"read_only_subpaths,omitempty"`
}

// ClonePolicySnapshot returns an isolated normalized copy of one snapshot.
func ClonePolicySnapshot(in PolicySnapshot) PolicySnapshot {
	out := in
	out.Route = Route(strings.TrimSpace(string(in.Route)))
	out.Backend = CanonicalBackend(in.Backend)
	out.Permission = Permission(strings.TrimSpace(string(in.Permission)))
	out.Isolation = Isolation(strings.TrimSpace(string(in.Isolation)))
	out.Network = Network(strings.TrimSpace(string(in.Network)))
	out.WritableRoots = normalizeStringSlice(in.WritableRoots)
	out.ReadOnlySubpaths = normalizeStringSlice(in.ReadOnlySubpaths)
	return out
}

// PolicySnapshotEmpty reports whether a snapshot carries no runtime facts.
func PolicySnapshotEmpty(in PolicySnapshot) bool {
	in = ClonePolicySnapshot(in)
	return in.Route == "" && in.Backend == "" && in.Permission == "" &&
		in.Isolation == "" && in.Network == "" && len(in.WritableRoots) == 0 &&
		len(in.ReadOnlySubpaths) == 0
}

// SummarizePolicy returns the model-facing facts needed to choose default
// execution versus Host approval without enumerating auxiliary write roots.
func SummarizePolicy(in PolicySnapshot) PolicySummary {
	in = ClonePolicySnapshot(in)
	return PolicySummary{
		Route:            in.Route,
		Permission:       in.Permission,
		Network:          in.Network,
		ReadOnlySubpaths: append([]string(nil), in.ReadOnlySubpaths...),
	}
}
