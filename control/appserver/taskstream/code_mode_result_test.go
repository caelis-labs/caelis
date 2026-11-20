package taskstream

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

func TestScriptFinalFrameProjectsSelectedContent(t *testing.T) {
	const selected = `{"answer":9007199254740993}`
	receipt := tool.Result{
		Content: []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(selected)}},
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	env := streamFinalFrameEvent(taskFrameProjectionRequest{TaskKind: task.KindCodeMode, ToolName: "js_exec", CallID: "script"}, controltaskstream.Frame{Closed: true, State: "completed", Text: string(raw)})
	update := env.Update.(eventstream.ToolCallUpdate)
	if len(update.Content) != 1 {
		t.Fatalf("final script update content = %+v, want one selected result", update.Content)
	}
	text, ok := update.Content[0].Content.(eventstream.TextContent)
	if !ok || text.Text != selected {
		t.Fatalf("final script update content = %+v, want exact selected result %s", update.Content, selected)
	}
}
