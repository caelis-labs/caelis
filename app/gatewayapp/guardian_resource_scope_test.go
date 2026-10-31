package gatewayapp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func guardianResourceRequest(t *testing.T) kernel.ApprovalReviewRequest {
	t.Helper()
	req := guardianWindowRequest(t, "resource")
	req.Approval.ToolName = "desktop_world__exec"
	req.Approval.RawInput = map[string]any{"app": "com.example.测试", "source": "return await app.windows();"}
	req.RuntimeRequest.Call.Input, _ = json.Marshal(req.Approval.RawInput)
	req.RuntimeRequest.Metadata = map[string]any{approval.MetadataMCPResourceGate: true}
	req.Approval.Options = []approval.Option{{ID: "allow_session", Kind: "allow_once"}, {ID: "cancel", Kind: "reject_once"}}
	req.RuntimeRequest.Tool = tool.Definition{Name: req.Approval.ToolName, Metadata: map[string]any{
		tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataPluginID: "desktop-world",
		tool.MetadataMCPServer: "desktop-world", tool.MetadataMCPTool: "exec",
		tool.MetadataMCPSourceFingerprint: "fixture-source", tool.MetadataMCPResourceParameter: "app",
	}}
	return req
}

func TestGuardianResourceScopeDisplaysExactArgumentAndSession(t *testing.T) {
	req := guardianResourceRequest(t)
	state, err := guardianScreenState(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := state["action"].(map[string]any)["mcp_source"].(map[string]any)
	want := map[string]any{"parameter": "app", "value": "com.example.测试"}
	if !reflect.DeepEqual(source["resource_scope"], want) {
		t.Fatalf("resource scope = %#v", source)
	}
	options := source["scope_options"].(map[string]any)
	if len(options) != 2 || options["allow_session"] == nil || options["cancel"] == nil {
		t.Fatalf("resource scope inherited broader source options: %#v", options)
	}
	planned, _, err := guardianPlannedActionJSON(req)
	if err != nil || !strings.Contains(planned, `"resource_scope"`) || !strings.Contains(planned, `com.example.测试`) || strings.Contains(planned, `"allow_always"`) {
		t.Fatalf("Guardian resource action = %s, %v", planned, err)
	}
	// Display arguments cannot replace the original resource selected by the
	// Runtime or provide authority for another app.
	req.Approval.RawInput["app"] = "display-only"
	if !reflect.DeepEqual(guardianMCPResourceScope(req), want) {
		t.Fatal("display input replaced the Runtime argument")
	}
}

func TestGuardianResourceScopeRequiresHostRuleAndExactNativeOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*kernel.ApprovalReviewRequest)
	}{
		{"per-call options collide with resource IDs", func(req *kernel.ApprovalReviewRequest) {
			delete(req.RuntimeRequest.Metadata, approval.MetadataMCPResourceGate)
		}},
		{"marker false", func(req *kernel.ApprovalReviewRequest) {
			req.RuntimeRequest.Metadata[approval.MetadataMCPResourceGate] = false
		}},
		{"marker wrong type", func(req *kernel.ApprovalReviewRequest) {
			req.RuntimeRequest.Metadata[approval.MetadataMCPResourceGate] = "true"
		}},
		{"no host rule", func(req *kernel.ApprovalReviewRequest) {
			delete(req.RuntimeRequest.Tool.Metadata, tool.MetadataMCPResourceParameter)
		}},
		{"external tool name", func(req *kernel.ApprovalReviewRequest) {
			delete(req.RuntimeRequest.Tool.Metadata, tool.MetadataToolKind)
		}},
		{"missing original argument", func(req *kernel.ApprovalReviewRequest) {
			req.RuntimeRequest.Call.Input = []byte(`{"source":"return 1;"}`)
		}},
		{"wrong argument type", func(req *kernel.ApprovalReviewRequest) {
			req.RuntimeRequest.Call.Input = []byte(`{"app":["com.example.测试"]}`)
		}},
		{"empty argument", func(req *kernel.ApprovalReviewRequest) { req.RuntimeRequest.Call.Input = []byte(`{"app":"  "}`) }},
		{"custom call options", func(req *kernel.ApprovalReviewRequest) { req.Approval.Options[0].ID = "confirm_sensitive" }},
		{"wrong option kind", func(req *kernel.ApprovalReviewRequest) { req.Approval.Options[0].Kind = "allow_always" }},
		{"ordinary source options on resource tool", func(req *kernel.ApprovalReviewRequest) {
			req.Approval.Options = []approval.Option{{ID: "allow_once", Kind: "allow_once"}, {ID: "allow_session", Kind: "allow_once"}, {ID: "allow_always", Kind: "allow_always"}, {ID: "cancel", Kind: "reject_once"}}
		}},
		{"extra scope", func(req *kernel.ApprovalReviewRequest) {
			req.Approval.Options = append(req.Approval.Options, approval.Option{ID: "allow_always", Kind: "allow_always"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := guardianResourceRequest(t)
			tc.edit(&req)
			if scope := guardianMCPResourceScope(req); scope != nil {
				t.Fatalf("unproven scope = %#v", scope)
			}
			if source := guardianMCPSource(req); source != nil && (source["resource_scope"] != nil || source["scope_options"] != nil) {
				t.Fatalf("unproven request acquired grant semantics: %#v", source)
			}
		})
	}
}

func TestGuardianResourceGrantNeedsDecisiveSessionChoice(t *testing.T) {
	for _, tc := range []struct {
		name        string
		probability float64
		allowed     bool
	}{{"decisive", .98, true}, {"uncertain cannot become once", .6, false}} {
		t.Run(tc.name, func(t *testing.T) {
			req := guardianResourceRequest(t)
			response := judgment.Response{Answers: guardianScreenAnswersWith(judgment.Answer{
				Type: judgment.Choice, Choice: "allow_session", Probabilities: map[string]float64{"allow_session": tc.probability, "cancel": 1 - tc.probability},
			}, 0, 0)}
			option, err := guardianScreenSelection(req, response)
			if tc.allowed {
				if err != nil || option.ID != "allow_session" {
					t.Fatalf("decisive resource grant = %+v, %v", option, err)
				}
			} else if err == nil {
				t.Fatalf("uncertain Session scope was treated as once: %+v", option)
			}
		})
	}
}
