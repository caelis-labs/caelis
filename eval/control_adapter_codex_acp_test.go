//go:build e2e

package eval

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	controladapter "github.com/OnslaughtSnail/caelis/app/gatewayapp/controladapter"
	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
)

func TestAdapterCodexACPModelEffortE2E(t *testing.T) {
	if os.Getenv("CAELIS_RUN_CODEX_ACP_E2E") != "1" {
		t.Skip("set CAELIS_RUN_CODEX_ACP_E2E=1 to run the real codex-acp e2e")
	}
	codexACP, err := exec.LookPath("codex-acp")
	if err != nil {
		t.Fatalf("codex-acp not found on PATH: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	workspace := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "codex-acp-e2e",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly: assembly.ResolvedAssembly{
			Agents: []assembly.AgentConfig{{
				Name:        "codex",
				Description: "real Codex ACP adapter",
				Command:     codexACP,
			}},
		},
		Model: gatewayapp.ModelConfig{
			Provider: "openai",
			API:      providers.APIOpenAI,
			Model:    "gpt-5.5",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "codex-acp-e2e-session", "surface", "gpt-5.5")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	agentStatus, err := driver.HandoffAgent(ctx, "codex")
	if err != nil {
		t.Fatalf("HandoffAgent(codex) error = %v", err)
	}
	if got := strings.ToLower(strings.TrimSpace(agentStatus.ControllerKind)); got != "acp" {
		t.Fatalf("controller kind = %q, want acp", agentStatus.ControllerKind)
	}
	model := strings.TrimSpace(agentStatus.ControllerModel)
	effort := strings.TrimSpace(agentStatus.ControllerReasoningEffort)
	t.Logf("agent status: model=%q effort=%q modelOptions=%d effortOptions=%d", model, effort, len(agentStatus.ControllerModels), len(agentStatus.ControllerEfforts))
	if model == "" || effort == "" {
		t.Logf("controller current model has no declared effort; falling back to a model with modelId-based effort choices")
	}
	if model == "" {
		t.Fatalf("controller model = %q, want populated", model)
	}
	if len(agentStatus.ControllerModels) == 0 {
		t.Fatalf("controller models = %#v, want remote model choices", agentStatus.ControllerModels)
	}

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if effort != "" {
		if got := strings.TrimSpace(status.ReasoningEffort); got != effort {
			t.Fatalf("Status().ReasoningEffort = %q, want %q", got, effort)
		}
		if want := "[" + strings.ToLower(effort) + "]"; !strings.Contains(strings.ToLower(status.Model), want) {
			t.Fatalf("Status().Model = %q, want effort suffix %s", status.Model, want)
		}
	}

	modelWithEffort := model
	effortCandidates, err := driver.CompleteSlashArg(ctx, "model use "+modelWithEffort, "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use %s) error = %v", modelWithEffort, err)
	}
	if len(effortCandidates) == 0 {
		for _, candidate := range agentStatus.ControllerModels {
			candidateModel := strings.TrimSpace(candidate.Value)
			if candidateModel == "" || strings.EqualFold(candidateModel, modelWithEffort) {
				continue
			}
			candidates, completeErr := driver.CompleteSlashArg(ctx, "model use "+candidateModel, "", 20)
			if completeErr != nil {
				t.Fatalf("CompleteSlashArg(model use %s) error = %v", candidateModel, completeErr)
			}
			if len(candidates) == 0 {
				continue
			}
			modelWithEffort = candidateModel
			effortCandidates = candidates
			break
		}
	}
	if len(effortCandidates) == 0 {
		t.Fatalf("effort candidates for remote models = %#v, want remote reasoning choices", agentStatus.ControllerModels)
	}
	if effort != "" && strings.EqualFold(modelWithEffort, model) && !slashCandidatesHaveValue(effortCandidates, effort) {
		t.Fatalf("effort candidates = %#v, want current effort %q", effortCandidates, effort)
	}
	selectedEffort := preferredEffortCandidate(effortCandidates)
	status, err = driver.UseModel(ctx, modelWithEffort, selectedEffort)
	if err != nil {
		t.Fatalf("UseModel(%s, %s) error = %v", modelWithEffort, selectedEffort, err)
	}
	if got := strings.TrimSpace(status.ReasoningEffort); !strings.EqualFold(got, selectedEffort) {
		t.Fatalf("Status().ReasoningEffort after UseModel = %q, want %q", got, selectedEffort)
	}
	if want := "[" + strings.ToLower(selectedEffort) + "]"; !strings.Contains(strings.ToLower(status.Model), want) {
		t.Fatalf("Status().Model after UseModel = %q, want effort suffix %s", status.Model, want)
	}
	agentStatus, err = driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus(after UseModel) error = %v", err)
	}
	if got := strings.TrimSpace(agentStatus.ControllerReasoningEffort); !strings.EqualFold(got, selectedEffort) {
		t.Fatalf("AgentStatus().ControllerReasoningEffort after UseModel = %q, want %q", got, selectedEffort)
	}
}

func preferredEffortCandidate(candidates []controladapter.SlashArgCandidate) string {
	for _, preferred := range []string{"xhigh", "high", "medium", "low"} {
		for _, candidate := range candidates {
			if strings.EqualFold(strings.TrimSpace(candidate.Value), preferred) {
				return strings.TrimSpace(candidate.Value)
			}
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return strings.TrimSpace(candidates[0].Value)
}
