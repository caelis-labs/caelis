package providers

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type openAICompatTool struct {
	Type     string                     `json:"type"`
	Function *openAICompatFunctionDecl  `json:"function,omitempty"`
	Extra    map[string]json.RawMessage `json:"-"`
}

func (t openAICompatTool) MarshalJSON() ([]byte, error) {
	obj := map[string]json.RawMessage{}
	if strings.TrimSpace(t.Type) != "" {
		raw, err := json.Marshal(t.Type)
		if err != nil {
			return nil, err
		}
		obj["type"] = raw
	}
	if t.Function != nil {
		raw, err := json.Marshal(t.Function)
		if err != nil {
			return nil, err
		}
		obj["function"] = raw
	}
	for key, raw := range t.Extra {
		key = strings.TrimSpace(key)
		if key == "" || key == "type" || key == "function" || raw == nil {
			continue
		}
		obj[key] = append(json.RawMessage(nil), raw...)
	}
	return json.Marshal(obj)
}

type openAICompatFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
}

type openAICompatToolCall struct {
	ID       string                   `json:"id"`
	Index    int                      `json:"index,omitempty"`
	Type     string                   `json:"type,omitempty"`
	Function openAICompatCallFunction `json:"function"`
}

type openAICompatCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func fromKernelTools(tools []model.ToolDefinition, strictFunctionTools bool) []openAICompatTool {
	out := make([]openAICompatTool, 0, len(tools))
	for _, t := range tools {
		parameters := t.Parameters
		strict := false
		if strictFunctionTools && t.Strict {
			if converted, ok := openAICompatStrictToolParameters(t.Parameters); ok {
				parameters = converted
				strict = true
			}
		}
		out = append(out, openAICompatTool{
			Type: "function",
			Function: &openAICompatFunctionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  parameters,
				Strict:      strict,
			},
		})
	}
	return out
}
