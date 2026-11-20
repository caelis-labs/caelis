package toolbinding

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
)

// IsViewImage recognizes the native image reader, including through the
// Runtime's outer tool wrapper. A display name alone does not bind it.
func IsViewImage(candidate tool.Tool) bool {
	if _, ok := candidate.(*filesystem.ViewImageTool); ok {
		return true
	}
	binding, ok := candidate.(interface{ RuntimeViewImage(Token) bool })
	return ok && binding.RuntimeViewImage(Token{})
}

// HideViewImage reports whether this native tool is unavailable to the model.
func HideViewImage(candidate tool.Tool, llm model.LLM) bool {
	if !IsViewImage(candidate) {
		return false
	}
	capabilities, _ := model.CapabilitiesOf(llm)
	return !capabilities.ImageInput
}
