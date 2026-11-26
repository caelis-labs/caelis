package tool

import "context"

// Source returns a concurrency-safe snapshot of ready tools. Returned tools and
// definitions must remain immutable. Reading a source must not wait for I/O.
type Source interface{ Tools() []Tool }

// NamedSource is an optional capability of a Source for looking up one current
// tool by its canonical name. Lookup must not wait for I/O.
// A result is a candidate, not a grant; callers still validate bindings and
// execution authority. Missing names must not resolve to another source's alias.
// Sources without this capability retain their ordinary enumeration fallback.
type NamedSource interface {
	Lookup(context.Context, string) (Tool, bool, error)
}
