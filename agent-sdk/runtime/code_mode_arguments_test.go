package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

// Exercise the real encoder, process protocol, Runtime admission and Write tool.
// The model supplies deterministic source; no live DTW or model is involved.
func TestCodeModeLargeWriteArguments(t *testing.T) {
	for _, tc := range []struct {
		name, call  string
		largeOutput bool
	}{
		{name: "direct", call: `Write(args)`},
		{name: "native", call: `tools.native.Write(args)`},
		{name: "tools_call", call: `tools.call("Write",args)`},
		{name: "output_budget", call: `Write(args)`, largeOutput: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			write, err := filesystem.NewWrite(backend)
			if err != nil {
				t.Fatal(err)
			}
			code := `const records=Array.from({length:80},(_,index)=>({index,text:"采集".repeat(600)}));const args={path:"records.json",content:JSON.stringify(records)};await ` + tc.call + `;`
			if tc.largeOutput {
				code += `return "x".repeat(2048);`
			} else {
				code += `return {written:records.length};`
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i != 0 {
					return nil
				}
				raw, _ := json.Marshal(map[string]string{"code": code})
				return []model.ToolCall{{ID: "large-write", Name: codemode.ToolName, Args: string(raw)}}
			}}
			limits := codemode.Limits{OutputBytes: 1024}
			runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{write, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Limits: limits}}}, nil, nil)
			type record struct {
				Index int    `json:"index"`
				Text  string `json:"text"`
			}
			var records []record
			for index := range 80 {
				records = append(records, record{index, strings.Repeat("采集", 600)})
			}
			want, err := json.Marshal(records)
			if err != nil {
				t.Fatal(err)
			}
			if len(want) <= 64<<10 || len(want) <= limits.OutputBytes {
				t.Fatal("fixture must exceed both 64 KiB and the configured output budget")
			}
			if len(driver.requests) != 2 {
				t.Fatalf("model requests=%d", len(driver.requests))
			}
			result := errorDeliveryModelResult(t, driver.requests[1], "large-write")
			output, _ := json.Marshal(result.Content)
			got, err := os.ReadFile(filepath.Join(root, "records.json"))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("Write did not preserve the %d-byte JSON content: bytes=%d err=%v output=%s", len(want), len(got), err, output)
			}
			t.Logf("verified %d-byte JSON Write", len(want))
			if result.IsError != tc.largeOutput {
				t.Fatalf("IsError=%v output=%s", result.IsError, output)
			}
			if tc.largeOutput {
				if !bytes.Contains(output, []byte("JSON byte budget exceeded")) {
					t.Fatalf("output budget was not enforced: %s", output)
				}
			} else if !bytes.Contains(output, []byte(`"written":80`)) {
				t.Fatalf("script did not complete: %s", output)
			}
		})
	}
}
