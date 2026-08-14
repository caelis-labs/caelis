package plugin

import (
	"fmt"
	"strings"
)

func upsertLocalPluginConfig(state *State, next Config) {
	if state == nil {
		return
	}
	if idx := pluginConfigIndexByID(state.Plugins, next.ID); idx >= 0 {
		state.Plugins[idx] = next
		return
	}
	state.Plugins = append(state.Plugins, next)
}

func upsertMarketplacePluginConfig(state *State, next Config) error {
	if state == nil {
		return nil
	}
	rootIdx := pluginConfigIndexByRoot(state.Plugins, next.Root)
	if rootIdx >= 0 {
		if idIdx := pluginConfigIndexByID(state.Plugins, next.ID); idIdx >= 0 && idIdx != rootIdx {
			return fmt.Errorf("plugin service: plugin id %q already exists at %s; cannot rename marketplace plugin at %s", strings.TrimSpace(next.ID), state.Plugins[idIdx].Root, next.Root)
		}
		state.Plugins[rootIdx] = next
		return nil
	}
	if idIdx := pluginConfigIndexByID(state.Plugins, next.ID); idIdx >= 0 {
		state.Plugins[idIdx] = next
		return nil
	}
	state.Plugins = append(state.Plugins, next)
	return nil
}

func pluginConfigIndexByID(configs []Config, id string) int {
	id = strings.TrimSpace(id)
	for i, configured := range configs {
		if strings.EqualFold(strings.TrimSpace(configured.ID), id) {
			return i
		}
	}
	return -1
}

func pluginConfigIndexByRoot(configs []Config, root string) int {
	for i, configured := range configs {
		if samePluginRoot(configured.Root, root) {
			return i
		}
	}
	return -1
}
