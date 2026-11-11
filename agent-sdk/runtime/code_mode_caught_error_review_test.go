package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func TestRuntimeReviewCaughtReadErrorCanContinue(t *testing.T) {
	store, active, root := codeModeStore(t)
	backend, err := host.New(host.Config{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	read, err := filesystem.NewRead(filesystem.DefaultReadConfig(), backend)
	if err != nil {
		t.Fatal(err)
	}
	goodPath := filepath.Join(root, "good.txt")
	if err = os.WriteFile(goodPath, []byte("valid-read-result"), 0600); err != nil {
		t.Fatal(err)
	}
	var firstErr, secondErr error
	declaration := &codemode.Tool{Name: codemode.ToolName, Executor: codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		bad, _ := json.Marshal(map[string]any{"path": root})
		_, firstErr = host.Call(ctx, "Read", bad)
		good, _ := json.Marshal(map[string]any{"path": goodPath})
		_, secondErr = host.Call(ctx, "Read", good)
		return []byte(`{"caught":true}`), nil
	})}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"fixture"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{declaration, read}}, nil, nil)
	for _, event := range events {
		if event.Tool != nil && event.Tool.ID == "script" && event.Tool.Status == "failed" {
			raw, _ := json.Marshal(event.Tool)
			t.Logf("public-tool=%s", raw)
			raw, _ = json.Marshal(event.Message)
			t.Logf("public-message=%s", raw)
		}
	}
	t.Logf("first=%v second=%v", firstErr, secondErr)
	if firstErr == nil {
		t.Fatal("directory read should fail")
	}
	if secondErr != nil {
		t.Fatalf("caught ordinary Read error closed Host admission: %v", secondErr)
	}
}
