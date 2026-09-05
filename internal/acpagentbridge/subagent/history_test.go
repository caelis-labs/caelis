package subagent

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestLoadedAgentCommunicationPromptRestoresCanonicalParentIdentity(t *testing.T) {
	t.Parallel()

	header := session.AgentCommunicationPromptHeader(session.ControllerExecutor(session.ControllerBinding{
		Kind: session.ControllerKindKernel, ControllerID: "sdk-kernel", AgentName: "local",
	}))
	for _, prompt := range []string{
		header,
		"[Internal agent message]\nSender: local\nKind: controller\nSender ID: sdk-kernel\nMessage:",
	} {
		got, body, ok := loadedAgentCommunicationPrompt(prompt + "\ncontinue from parent")
		if !ok || got != session.ParentCommunicationActor() || body != "continue from parent" {
			t.Fatalf("loaded parent communication = (%#v, %q, %v), want canonical parent identity", got, body, ok)
		}
	}
	if strings.Contains(header, "local") {
		t.Fatalf("parent prompt leaked local controller name: %q", header)
	}
}
