package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/memorybinding"
	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

const workspaceMemoryLabelPrefix = "caelis.workspace.sha256:"

// MemoryLabelSelectionContext is embedding-owned context for appending opaque
// product labels to the mandatory workspace partition. It carries no Memory
// credential or capability.
type MemoryLabelSelectionContext struct {
	SessionRef session.SessionRef
	Workspace  session.WorkspaceRef
	BindingRef memorybinding.BindingRef
}

// MemoryLabelSelector lets an embedding append opaque labels for future
// product concepts. Caelis always retains its own workspace label, validates
// the combined set through Memory, and never exposes the labels to the model.
type MemoryLabelSelector func(context.Context, MemoryLabelSelectionContext) ([]string, error)

func bindRuntimeMemoryLabels(
	ctx context.Context,
	binding memorybinding.RuntimeMemoryBindingSnapshot,
	selector MemoryLabelSelector,
	ref session.SessionRef,
	workspace session.WorkspaceRef,
) (memorybinding.RuntimeMemoryBindingSnapshot, error) {
	workspaceKey := strings.TrimSpace(workspace.Key)
	if workspaceKey == "" {
		return memorybinding.RuntimeMemoryBindingSnapshot{}, fmt.Errorf("gatewayapp: Memory workspace key is required")
	}
	digest := sha256.Sum256([]byte(workspaceKey))
	labels := memoryv1alpha1.LabelSet{
		memoryv1alpha1.Label(workspaceMemoryLabelPrefix + hex.EncodeToString(digest[:])),
	}
	if selector != nil {
		additional, err := selector(ctx, MemoryLabelSelectionContext{
			SessionRef: ref,
			Workspace:  workspace,
			BindingRef: binding.BindingRef,
		})
		if err != nil {
			return memorybinding.RuntimeMemoryBindingSnapshot{}, fmt.Errorf("gatewayapp: select Runtime Memory labels: %w", err)
		}
		for _, label := range additional {
			labels = append(labels, memoryv1alpha1.Label(label))
		}
	}
	labeled, err := memorybinding.BindRuntimeLabels(binding, labels)
	if err != nil {
		return memorybinding.RuntimeMemoryBindingSnapshot{}, fmt.Errorf("gatewayapp: bind Runtime Memory labels: %w", err)
	}
	return labeled, nil
}
