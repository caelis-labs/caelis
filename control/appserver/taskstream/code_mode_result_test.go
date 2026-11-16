package taskstream

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

func TestScriptFinalFrameProjectsSelectedContent(t *testing.T) {
	receipt := tool.Result{Content: []model.Part{model.NewJSONPart(json.RawMessage(`{"answer":42}`))}, Metadata: map[string]any{"code_mode_script_completion": "completed", "code_mode_source": map[string]any{"code": "return {answer:42};"}}}
	raw, _ := json.Marshal(receipt)
	env := streamFinalFrameEvent(taskFrameProjectionRequest{TaskKind: task.KindCodeMode, ToolName: "js_exec", CallID: "script"}, controltaskstream.Frame{Closed: true, State: "completed", Text: string(raw)})
	update := env.Update.(eventstream.ToolCallUpdate)
	encoded, _ := json.Marshal(update.Content)
	if len(update.Content) != 1 || !strings.Contains(string(encoded), `answer`) {
		t.Fatalf("selected result missing; raw receipt leaked to terminal instead: %+v", update)
	}
	if strings.Contains(string(encoded), "code_mode_source") {
		t.Fatal("internal receipt rendered as business return")
	}
}
