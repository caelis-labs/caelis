package plugin

// Available selects the same package as Select, but a failed selection disables only that plugin.
// The selected source and its cause remain observable; no fallback is activated.
// Explicit configuration validation uses Select instead.
func (load BuiltinPackage) Available(optional Contributions, paths RuntimePaths, defaults ...Config) Contributions {
	for _, source := range optional.Sources {
		if source.ID == "desktop-world" && source.Error != "" {
			return optional
		}
	}
	combined, err := load.Select(optional, paths, defaults...)
	if err == nil {
		return combined
	}
	for _, source := range optional.Sources {
		if source.ID == "desktop-world" {
			optional.Disable(source.ID, err)
			return optional
		}
	}
	source := Source{ID: "desktop-world", Status: "failed", Error: err.Error()}
	for _, configured := range defaults {
		if configured.Enabled && IsProductPlugin(configured) {
			source.Root, source.Version = configured.Root, configured.Version
			break
		}
	}
	optional.Sources = append([]Source{source}, optional.Sources...)
	return optional
}
