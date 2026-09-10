package collaboration

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestToolSchemasSurviveModelRequestCloning(t *testing.T) {
	specs := tool.ModelSpecs(Tools(true, nil))
	cloned := model.CloneRequest(&model.Request{Tools: specs})
	for i, spec := range specs {
		before, err := json.Marshal(spec.Function.Parameters)
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(cloned.Tools[i].Function.Parameters)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("%s schema changed during request cloning: %s -> %s", spec.Function.Name, before, after)
		}
	}
}
