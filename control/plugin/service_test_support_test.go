package plugin

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

type recordedMutation struct {
	GuardAction   string
	FailureAction string
	Reconfigure   bool
}

type memoryHost struct {
	dir       string
	state     State
	statuses  map[string][]mcp.MCPServerInfo
	loadErr   error
	loadCalls int
	updateErr error
	mutations []recordedMutation
}

func (h *memoryHost) StoreDir() string {
	return h.dir
}

func (h *memoryHost) LoadPluginState(context.Context) (State, error) {
	h.loadCalls++
	if h.loadErr != nil {
		return State{}, h.loadErr
	}
	return h.state.Clone(), nil
}

func (h *memoryHost) UpdatePluginState(_ context.Context, mutation Mutation) error {
	if h.updateErr != nil {
		return h.updateErr
	}
	if mutation.Apply == nil {
		return errors.New("memory host: mutation apply is required")
	}
	next := h.state.Clone()
	if err := mutation.Apply(&next); err != nil {
		return err
	}
	h.state = next
	h.mutations = append(h.mutations, recordedMutation{
		GuardAction:   mutation.GuardAction,
		FailureAction: mutation.FailureAction,
		Reconfigure:   mutation.Reconfigure,
	})
	if mutation.AfterCommit != nil {
		return mutation.AfterCommit(h.state.Clone())
	}
	return nil
}

func (h *memoryHost) MCPServersStatus(pluginID string) []mcp.MCPServerInfo {
	return append([]mcp.MCPServerInfo(nil), h.statuses[pluginID]...)
}
