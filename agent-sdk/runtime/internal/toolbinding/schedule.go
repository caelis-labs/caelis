package toolbinding

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
)

// NativeSchedule describes ordering within a native model batch only. The
// optional script IDs identify a wait and its matching cancellation, not an
// authorization to perform either operation.
type NativeSchedule struct {
	Ordered      bool
	ScriptWait   string
	ScriptCancel string
}

// ScheduleNativeCall classifies the concrete target and prepared arguments.
func ScheduleNativeCall(ctx context.Context, candidate tool.Tool, call tool.Call) NativeSchedule {
	switch owner := candidate.(type) {
	case interface {
		RuntimeNativeSchedule(Token, context.Context, tool.Call) NativeSchedule
	}:
		return owner.RuntimeNativeSchedule(Token{}, ctx, call)
	case *filesystem.PatchTool, *filesystem.WriteTool:
		// The file lock protects the mutation, not model order of acquisition.
		return NativeSchedule{Ordered: true}
	default:
		return NativeSchedule{Ordered: candidate.Definition().MayCompleteTurn}
	}
}
