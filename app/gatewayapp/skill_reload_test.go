package gatewayapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestNativeSkillInstructionsReloadOnFirstRequestUsingCompact(t *testing.T) {
	ctx := t.Context()
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, ".agents", "skills")
	write := func(name, description string) string {
		t.Helper()
		root := filepath.Join(skillDir, name)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+description+"\n---\nSKILL_BODY_PRIVATE\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	old := write("initial-skill", "Initial description.")
	provider := newGatewayAppCompactionOllamaServer(t)
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis", UserID: "owner", StoreDir: t.TempDir(), WorkspaceCWD: workspace,
		SkillDirs: []string{skillDir}, ContextWindow: 65536,
		Model: ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "compact-test", BaseURL: provider.URL, HTTPClient: provider.Client()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	active, err := startGatewayAppTestSession(ctx, stack, "skill reload")
	if err != nil {
		t.Fatal(err)
	}
	prompt := func(operation, input string) (string, string) {
		t.Helper()
		if _, err := runHeadlessOnceForGatewayAppTest(ctx, stack, active, operation, input, headless.Options{}); err != nil {
			t.Fatal(err)
		}
		request := provider.lastNormal.Load()
		if request == nil {
			t.Fatal("no provider request")
		}
		prefix, history, ok := strings.Cut(*request, "\nuser: ")
		if !ok || strings.Contains(prefix, "SKILL_BODY_PRIVATE") {
			t.Fatalf("invalid model prefix: %s", prefix)
		}
		return prefix, history
	}
	before, _ := prompt("first-skill-prompt", "Begin with initial Skill metadata.")
	if !strings.Contains(before, "initial-skill: Initial description.") {
		t.Fatalf("missing initial Skill metadata: %s", before)
	}
	if err := os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	write("next-skill", "New description.")
	if current, _ := prompt("second-skill-prompt", "Continue with the same system prompt."); current != before {
		t.Fatal("ordinary Turn changed the system prompt after a Skill directory edit")
	}
	current := mustCurrentSession(t, stack, active.SessionID)
	result, err := stack.ControlClient().CompactSession(ctx, appserver.Principal{ID: active.UserID}, appserver.CompactSessionRequest{WriteBase: appserver.WriteBase{
		OperationID: "skill-reload-compact", SessionID: active.SessionID, ExpectedRevision: &current.Revision, ExpectedControllerEpoch: current.Controller.EpochID,
	}})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || provider.compactionCalls.Load() != 1 {
		t.Fatalf("manual compaction = %+v, %v", result, err)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if text, ok := pinnedSkillInstructions(state, ""); err != nil || !ok || !strings.Contains(text, "initial-skill") {
		t.Fatalf("Skill instructions changed during compaction generation or commit: %q, %v", text, err)
	}
	after, history := prompt("post-compact-skill-prompt", "Resume after compaction.")
	if !strings.Contains(history, "CONTEXT CHECKPOINT") || !strings.Contains(after, "next-skill: New description.") || strings.Contains(after, "initial-skill:") {
		t.Fatalf("first request with the compacted history missed the new Skill metadata: %s\n%s", after, history)
	}
	write("next-skill", "Another description.")
	if replay, _ := prompt("replay-skill-prompt", "Continue from the same checkpoint."); replay != after {
		t.Fatal("another request using the same checkpoint changed the system prompt")
	}
}
