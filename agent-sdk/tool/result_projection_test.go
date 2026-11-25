package tool_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/artifact"
)

func TestResultProjectionAddsTrustedArtifactReferences(t *testing.T) {
	t.Parallel()
	budget := tool.DefaultTruncationPolicy().ByteBudget()
	for _, tc := range []struct {
		name      string
		raw       string
		extension string
		collision bool
	}{
		{name: "text", raw: strings.Repeat("text evidence\n", budget/8), extension: ".txt"},
		{name: "scalar JSON", raw: strconv.Quote(strings.Repeat("scalar evidence", budget/4)), extension: ".json"},
		{
			name:      "namespace collision",
			raw:       `{"result":"` + strings.Repeat("evidence", budget) + `","system_hint":"tool-owned diagnostic","_caelis":{"tool_owned":true}}`,
			extension: ".json",
			collision: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var block tool.ContentBlock = tool.Text(tc.raw)
			if tc.extension == ".json" {
				block = tool.StructuredContent{Value: json.RawMessage(tc.raw)}
			}
			projection := tool.ResultProjection{Artifacts: artifact.NewFileStore(t.TempDir())}
			result, _ := projection.Project(t.Context(), tool.Result{Content: []tool.ContentBlock{block}})
			var path string
			if tc.extension == ".txt" {
				const prefix = "Caelis runtime artifact: "
				for _, content := range result.Content {
					text, ok := content.(tool.Text)
					if !ok {
						continue
					}
					if _, hint, found := strings.Cut(string(text), prefix); found {
						if strings.Contains(string(text), "System hint:") {
							t.Fatalf("artifact reference claims system-hint authority: %q", text)
						}
						path, _, _ = strings.Cut(hint, "\n")
						break
					}
				}
			} else {
				raw, ok := result.Structured()
				if !ok {
					t.Fatal("projected result has no structured content")
				}
				var payload struct {
					Result     *string `json:"result"`
					SystemHint string  `json:"system_hint"`
					Caelis     map[string]map[string]struct {
						Path      string `json:"path"`
						Truncated bool   `json:"model_visible_content_truncated"`
						Collision bool   `json:"tool_namespace_collision"`
					} `json:"_caelis"`
				}
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				metadata := payload.Caelis["runtime"]["artifact"]
				if payload.Result == nil || !metadata.Truncated || metadata.Collision != tc.collision {
					t.Fatalf("projected result = %+v, want wrapped result and trusted artifact metadata", payload)
				}
				if tc.collision && payload.SystemHint != "tool-owned diagnostic" {
					t.Fatalf("system_hint = %q, want tool-owned diagnostic", payload.SystemHint)
				}
				path = metadata.Path
			}
			if !filepath.IsAbs(path) || filepath.Ext(path) != tc.extension {
				t.Fatalf("artifact path = %q, want absolute path with extension %s", path, tc.extension)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, []byte(tc.raw)) {
				t.Fatalf("artifact has %d bytes, want original %d bytes", len(got), len(tc.raw))
			}
		})
	}
}

func TestResultProjectionOmitsArtifactReferenceWithoutSave(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "artifacts")
	projection := tool.ResultProjection{Artifacts: artifact.NewFileStore(dir)}
	small := tool.Text("small")
	result, _ := projection.Project(t.Context(), tool.Result{Content: []tool.ContentBlock{small}})
	if len(result.Content) != 1 || result.Content[0] != small {
		t.Fatalf("untruncated result = %#v, want original text", result)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("artifact directory stat error = %v, want not exist", err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	large := tool.Text(strings.Repeat("large", tool.DefaultTruncationPolicy().ByteBudget()))
	result, _ = projection.Project(t.Context(), tool.Result{Content: []tool.ContentBlock{large}})
	for _, part := range tool.ModelParts(result.Content) {
		if part.Text != nil && strings.Contains(part.Text.Text, "Caelis runtime artifact: ") {
			t.Fatalf("write-failed result references an unwritten artifact: %#v", result)
		}
	}
}
