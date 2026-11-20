package toolbinding

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
)

// Dispatch carries Runtime-owned inputs alongside, not inside, a tool call.
type Dispatch struct {
	Model model.LLM
	Step  *agent.ModelStepRef
}

type runtimeCaller interface {
	RuntimeCall(Token, context.Context, tool.Call, Dispatch) (tool.Result, error)
}

// MayCompleteTurn checks the effective call when its Runtime owner can narrow
// the tool's declared disposition (for example, a Task observing a command).
func MayCompleteTurn(ctx context.Context, candidate tool.Tool, call tool.Call) bool {
	if owner, ok := candidate.(interface {
		RuntimeMayCompleteTurn(Token, context.Context, tool.Call) bool
	}); ok {
		return owner.RuntimeMayCompleteTurn(Token{}, ctx, call)
	}
	return candidate.Definition().MayCompleteTurn
}

// Call forwards dispatch inputs only through Runtime-owned wrappers. It never
// puts them in a tool call or context.
func Call(ctx context.Context, candidate tool.Tool, call tool.Call, dispatch Dispatch) (tool.Result, error) {
	if rt, ok := candidate.(runtimeCaller); ok {
		return rt.RuntimeCall(Token{}, ctx, call, dispatch)
	}
	if dispatch.Model != nil {
		switch native := candidate.(type) {
		case *web.SearchTool:
			return native.CallWithBackend(ctx, call, searchBackend(dispatch.Model))
		case *filesystem.ViewImageTool:
			return native.CallWithImageInput(ctx, call, !HideViewImage(native, dispatch.Model))
		}
	}
	return candidate.Call(ctx, call)
}

func searchBackend(llm model.LLM) web.SearchBackend {
	var backend web.SearchBackend
	if provider, ok := llm.(interface{ ProviderName() string }); ok {
		backend.Provider = provider.ProviderName()
	}
	if availability, ok := llm.(model.WebSearchAvailability); ok {
		backend.UnavailableReason = availability.WebSearchUnavailableReason()
	}
	if searcher, ok := llm.(model.WebSearcher); ok {
		backend.SearchWeb = searcher.SearchWeb
	}
	return backend
}
