package gatewayapp

import (
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationNativeToolSelectionPreservesSDKOrder(t *testing.T) {
	for _, test := range []struct {
		name      string
		selection []string
		want      []string
	}{
		{"default", nil, []string{"Read", "ViewImage", "Write", "Patch", "Glob", "Grep", "RunCommand", "Task", "js_exec", "ReadResource", "PublishArtifact"}},
		{"empty", []string{}, []string{"ReadResource", "PublishArtifact"}},
		{"subset", []string{"js_exec", "Task", "RunCommand", "Read"}, []string{"Read", "RunCommand", "Task", "js_exec", "ReadResource", "PublishArtifact"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools, err := newApplicationNativeTools(&applicationExecutionRuntime{}, &sessionprofile.Store{}, sessionprofile.Binding{}, t.TempDir(), test.selection)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, candidate := range tools {
				name := candidate.Definition().Name
				names = append(names, name)
				if name == "RunCommand" {
					if _, ok := candidate.(*shell.RunCommandTool); !ok {
						t.Fatalf("RunCommand lost its SDK lifecycle owner: %T", candidate)
					}
				}
			}
			if !slices.Equal(names, test.want) {
				t.Fatalf("assembled tools = %v, want %v", names, test.want)
			}
		})
	}
}
