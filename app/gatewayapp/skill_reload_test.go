package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestSkillInstructionsRejectedAdmissionDoesNotPinNewSummary(t *testing.T) {
	ctx := t.Context()
	store := inmemory.NewStore(inmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	stamp := time.Unix(1_700_000_000, 0)
	write := func(description string) {
		t.Helper()
		path := filepath.Join(root, "SKILL.md")
		if err := os.WriteFile(path, []byte("---\nname: admission-test\ndescription: "+description+"\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	write("previous skill metadata")
	resolve := sessionSkillInstructions(store, active.SessionRef, func(context.Context) (skill.Catalog, error) {
		meta, err := skillfs.DiscoverRootMeta(root)
		return skill.NewCatalog([]skill.Meta{meta}), err
	})
	_, persist, err := resolve(ctx, "previous-summary")
	if err != nil || persist == nil {
		t.Fatalf("prepare previous Skill instructions: %v", err)
	}
	if err := persist(ctx); err != nil {
		t.Fatal(err)
	}
	write("metadata from the rejected assembly")
	var admissions, starts int
	var instructions string
	ctx = model.WithInvocationStart(ctx, func(_ context.Context, _ model.Invocation, req *model.Request) error {
		starts++
		if req.BaseInstructionCount != 2 || len(req.Instructions) != 2 || req.Instructions[0].Text.Text != "configured base" {
			t.Fatalf("configured base or Skill guidance changed: %+v", req)
		}
		instructions = req.Instructions[1].Text.Text
		return nil
	})
	chatAgent, err := (chat.Factory{}).NewAgent(ctx, agent.AgentSpec{
		ResolveSessionInstructions: resolve,
		ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
			return agent.ModelRequestSnapshot{Model: systemManagedAgentResponseModel{}, Instructions: []model.Part{model.NewTextPart("configured base")}, Admit: func(context.Context, agent.ModelRequestAdmission) error {
				admissions++
				if admissions == 1 {
					state, err := store.SnapshotState(ctx, active.SessionRef)
					if _, pinned := pinnedSkillInstructions(state, "new-summary"); err != nil || pinned {
						t.Errorf("unadmitted assembly already pinned the new summary: %v, %v", pinned, err)
					}
					write("metadata from the admitted assembly")
					return agent.ErrModelRequestSnapshotStale
				}
				return nil
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range chatAgent.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: []*session.Event{
		{ID: "new-summary", Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical, Text: "CONTEXT CHECKPOINT"},
	}})) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if admissions != 2 || starts != 1 || !strings.Contains(instructions, "metadata from the admitted assembly") {
		t.Fatalf("admissions=%d provider starts=%d instructions=%q", admissions, starts, instructions)
	}
	state, err := store.SnapshotState(ctx, active.SessionRef)
	if text, pinned := pinnedSkillInstructions(state, "new-summary"); err != nil || !pinned || text != instructions {
		t.Fatalf("persisted instructions = %q, pinned = %v, error = %v", text, pinned, err)
	}
}

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
		Model: ModelConfig{Provider: "ollama", API: model.APIOllama, Model: "compact-test", BaseURL: provider.URL, HTTPClient: provider.Client()},
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
	if err := stack.sessionRuntimes.releaseSession(ctx, active.SessionID); err != nil {
		t.Fatal(err)
	}
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
