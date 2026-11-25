// Package toolpresentation publishes built-in display observations to the current
// Runtime invocation. These values are never included in model or JS content.
package toolpresentation

import "context"

type key struct{}

// With binds the display observer for this invocation.
func With(ctx context.Context, observe func(map[string]any)) context.Context {
	return context.WithValue(ctx, key{}, observe)
}

func publish(ctx context.Context, fields map[string]any) {
	if observe, _ := ctx.Value(key{}).(func(map[string]any)); observe != nil {
		observe(fields)
	}
}

// Read records the file path and whether the read reached its end.
func Read(ctx context.Context, path string, exhausted bool) {
	publish(ctx, map[string]any{"path": path, "exhausted": exhausted})
}

// Glob records the scope and summary of a file lookup.
func Glob(ctx context.Context, pattern, path, resolved string, count int, truncated bool, message string) {
	fields := map[string]any{"pattern": pattern, "path": path, "resolved_pattern": resolved, "count": count, "truncated": truncated}
	if message != "" {
		fields["message"] = message
	}
	publish(ctx, fields)
}

// SearchStats summarizes the files examined by Grep.
type SearchStats struct {
	Count, FileCount, FilesSelected, FilesScanned, BinaryFilesSkipped int
	Truncated                                                         bool
}

// Grep records the search scope and scan statistics.
func Grep(ctx context.Context, path, pattern, include, message string, stats SearchStats) {
	fields := map[string]any{
		"path": path, "pattern": pattern, "truncated": stats.Truncated,
		"count": stats.Count, "file_count": stats.FileCount,
		"files_selected": stats.FilesSelected, "files_scanned": stats.FilesScanned,
		"binary_files_skipped": stats.BinaryFilesSkipped,
	}
	if include != "" {
		fields["include"] = include
	}
	if message != "" {
		fields["message"] = message
	}
	publish(ctx, fields)
}

// Skill records the activated skill and its directory.
func Skill(ctx context.Context, name, path, root string) {
	publish(ctx, map[string]any{"name": name, "path": path, "root": root})
}

// WebFetch records the requested and resolved addresses.
func WebFetch(ctx context.Context, url, final, format string) {
	publish(ctx, map[string]any{"url": url, "final_url": final, "format": format})
}

// WebSearch records the provider used for the search.
func WebSearch(ctx context.Context, query, provider, model string, usage map[string]any) {
	fields := map[string]any{"query": query, "provider": provider}
	if usage != nil {
		fields["usage"] = usage
		fields["model"] = model
	}
	publish(ctx, fields)
}

// Command records the execution route and backend.
func Command(ctx context.Context, route, backend string) {
	fields := map[string]any{}
	if route != "" {
		fields["route"] = route
	}
	if backend != "" {
		fields["backend"] = backend
	}
	if len(fields) > 0 {
		publish(ctx, fields)
	}
}
