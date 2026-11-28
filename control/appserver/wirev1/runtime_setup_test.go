package wirev1

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
)

func TestRuntimeSetupProjectsActionableStepsWithoutTutorialLink(t *testing.T) {
	setup := agents.RuntimeSetup{Command: "fixture-acp", Directory: "/user/tools",
		ArchiveURL:    "https://example.invalid/fixture-acp.zip",
		InstallPrompt: "Install the official runtime into /user/tools and verify ACP initialize.",
		ManualSteps:   []string{"Extract the entire ZIP.", "cd '/user/tools'\nchmod +x fixture-acp"}}
	data := mustMarshalWire(t, appserver.SlashArgCandidate{Value: "confirm", RuntimeSetup: &setup})
	var dto generated.SlashArgCandidate
	if err := json.Unmarshal(data, &dto); err != nil {
		t.Fatal(err)
	}
	if dto.RuntimeSetup == nil || dto.RuntimeSetup.ArchiveUrl == nil || *dto.RuntimeSetup.ArchiveUrl != setup.ArchiveURL || !reflect.DeepEqual(dto.RuntimeSetup.ManualSteps, setup.ManualSteps) {
		t.Fatalf("wire lost manual installation steps: %s", data)
	}
	if bytes.Contains(data, []byte("documentation_url")) {
		t.Fatalf("wire still offers an unrelated tutorial: %s", data)
	}
	if dto.RuntimeSetup.InstallPrompt == nil || *dto.RuntimeSetup.InstallPrompt != setup.InstallPrompt {
		t.Fatalf("wire lost agent installation prompt: %s", data)
	}
}
