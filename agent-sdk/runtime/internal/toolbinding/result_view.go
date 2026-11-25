package toolbinding

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ProjectResult builds the bounded view of a completed call and applies ModelView
// once. A hook failure returns a safe message paired with the same tool call.
// Nested calls use the same budget and keep their full return value for the script.
func ProjectResult(ctx context.Context, def tool.Definition, call tool.Call, result tool.Result, cause error, project func(context.Context, tool.Result) (tool.Result, map[string]any)) (model.Message, map[string]any, error) {
	bounded, meta := project(ctx, result)
	message := ResultMessage(model.ToolCall{ID: call.ID, Name: call.Name}, bounded, cause)
	view, err := hookrun.ModelView(ctx, def, call, message)
	return view, meta, err
}

// ResultMessage pairs a canonical result with its originating call.
func ResultMessage(call model.ToolCall, result tool.Result, err error) model.Message {
	parts := tool.ModelParts(result.Content)
	if len(parts) == 0 {
		parts = []model.Part{model.NewJSONPart([]byte(`{}`))}
	}
	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{{
			Kind: model.PartKindToolResult,
			ToolResult: &model.ToolResultPart{
				ToolUseID: strings.TrimSpace(call.ID),
				Name:      strings.TrimSpace(call.Name),
				Content:   parts,
				IsError:   err != nil,
			},
		}},
	}
	return message
}
