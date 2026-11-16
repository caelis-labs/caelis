package runtime

import (
	"bytes"
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
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestCodeModeNativeBusinessResult(t *testing.T) {
	for _, code := range []string{
		`const response=await Read({path:"input.txt"});const file=response.json();return {content:file.content,done:!file.has_more};`,
		`let failure;try{await Read({path:"missing.txt"})}catch(e){failure={code:e.code,message:e.message};}const response=await Read({path:"input.txt"});const file=response.json();return {content:file.content,done:!file.has_more,failure};`,
	} {
		t.Run(code, func(t *testing.T) {
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
			if err = os.WriteFile(filepath.Join(root, "input.txt"), []byte("business-value"), 0600); err != nil {
				t.Fatal(err)
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					raw, _ := json.Marshal(map[string]any{"code": code})
					return []model.ToolCall{{ID: "business", Name: codemode.ToolName, Args: string(raw)}}
				}
				return nil
			}}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{read, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}, nil, nil)
			if len(driver.requests) != 2 {
				t.Fatalf("requests=%d", len(driver.requests))
			}
			raw, _ := json.Marshal(driver.requests[1].Messages)
			if !bytes.Contains(raw, []byte(`"content":"1: business-value"`)) || !bytes.Contains(raw, []byte(`"done":true`)) {
				t.Fatalf("business result not available: %s", raw)
			}
		})
	}
}
