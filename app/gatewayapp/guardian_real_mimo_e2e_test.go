package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const realMimoGuardianRepetitionsEnv = "CAELIS_REAL_MIMO_GUARDIAN_REPETITIONS"

// TestGuardianRealMimoReviewWithoutOutputBudget is an opt-in live acceptance
// test for the Terminal-Bench Guardian failure shape. It uses one copied
// managed credential and ModelProfile, leaves Agent bindings empty so Guardian
// follows the normal default-profile fallback, and verifies that the production
// review reaches the provider without a Guardian-specific output-token limit.
func TestGuardianRealMimoReviewWithoutOutputBudget(t *testing.T) {
	if strings.TrimSpace(os.Getenv(realMimoE2EEnabledEnv)) != "1" {
		t.Skip("set CAELIS_REAL_MIMO_E2E=1 to run the real-provider Guardian reproduction")
	}
	sourceStore := strings.TrimSpace(os.Getenv(realMimoE2ESourceStoreEnv))
	if sourceStore == "" {
		t.Fatalf("%s is required", realMimoE2ESourceStoreEnv)
	}
	profileID := strings.TrimSpace(os.Getenv(realMimoE2EProfileEnv))
	if profileID == "" {
		profileID = defaultRealMimoProfile
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	e2eStore := t.TempDir()
	profile, _ := copyRealMimoConfiguration(t, ctx, sourceStore, e2eStore, profileID)
	productionEffort := profile.Effort.DefaultEffort
	if profile.SupportsEffort("high") {
		productionEffort = "high"
	}
	workspace := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName:            "caelis",
		UserID:             "guardian-real-mimo-e2e",
		StoreDir:           e2eStore,
		WorkspaceKey:       "guardian-real-mimo-e2e",
		WorkspaceCWD:       workspace,
		ApprovalMode:       "auto-review",
		ModelProfileID:     profile.ID,
		ModelProfileEffort: productionEffort,
		SkillDirs:          []string{},
		Sandbox:            SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stack.Close(); err != nil {
			t.Errorf("close real Mimo Guardian stack: %v", err)
		}
	})

	resolved, handled, err := stack.resolveSystemAgentModel(ctx, agentbinding.HandleGuardian, 0)
	if err != nil {
		t.Fatalf("resolve Guardian model: %v", err)
	}
	if !handled || resolved.Model == nil {
		t.Fatal("Guardian did not resolve through the default ModelProfile fallback")
	}
	productionModel := withSystemAgentReasoningEffort(resolved)

	repetitions := realMimoGuardianRepetitions(t)
	for repetition := 1; repetition <= repetitions; repetition++ {
		repetition := repetition
		t.Run(fmt.Sprintf("run-%d", repetition), func(t *testing.T) {
			activeSession, err := startGatewayAppTestSession(
				ctx,
				stack,
				fmt.Sprintf("guardian-mimo-unbounded-%d", repetition),
			)
			if err != nil {
				t.Fatal(err)
			}
			appendGuardianRealMimoTranscript(t, ctx, stack.Sessions, activeSession)

			diagnostic := &guardianDiagnosticModel{}
			var guardianModel model.LLM
			if reasoned, ok := productionModel.(*systemAgentReasoningModel); ok {
				diagnostic.inner = reasoned.inner
				guardianModel = &systemAgentReasoningModel{inner: diagnostic, effort: reasoned.effort}
			} else {
				diagnostic.inner = productionModel
				guardianModel = diagnostic
			}
			reviewer, ok := newGuardianApprovalReviewer(stack.Sessions).(*guardianApprovalReviewer)
			if !ok {
				t.Fatal("Guardian reviewer has unexpected implementation")
			}
			req := guardianRealMimoApprovalRequest(activeSession, guardianModel, repetition)
			startedAt := time.Now()
			_, _, assistantEvent, parsed, _, reviewErr := reviewer.runGuardianReview(ctx, req)
			observations := diagnostic.snapshot()
			t.Logf(
				"Guardian acceptance profile=%s effort=%s elapsed=%s observations=%+v error=%v",
				profile.ID,
				productionEffort,
				time.Since(startedAt).Round(time.Millisecond),
				observations,
				reviewErr,
			)
			if reviewErr != nil {
				t.Fatalf("Guardian review failed: %v", reviewErr)
			}
			if assistantEvent == nil || strings.TrimSpace(session.EventText(assistantEvent)) == "" {
				t.Fatal("Guardian returned no visible final assessment")
			}
			if outcome := strings.ToLower(strings.TrimSpace(parsed.Outcome)); outcome != "allow" && outcome != "deny" {
				t.Fatalf("Guardian outcome = %q, want allow or deny", parsed.Outcome)
			}
			if len(observations) == 0 {
				t.Fatal("Guardian diagnostic observed no provider response")
			}
			for _, observation := range observations {
				if observation.MaxOutputTokens != 0 {
					t.Fatalf("Guardian MaxOutputTokens = %d, want unset", observation.MaxOutputTokens)
				}
				if observation.Effort != strings.TrimSpace(resolved.ReasoningEffort) {
					t.Fatalf("Guardian reasoning effort = %q, want resolved effort %q", observation.Effort, resolved.ReasoningEffort)
				}
			}
		})
	}
}

func realMimoGuardianRepetitions(t *testing.T) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(realMimoGuardianRepetitionsEnv))
	if raw == "" {
		return 3
	}
	repetitions, err := strconv.Atoi(raw)
	if err != nil || repetitions < 1 || repetitions > 10 {
		t.Fatalf("%s=%q, want integer 1..10", realMimoGuardianRepetitionsEnv, raw)
	}
	return repetitions
}

func appendGuardianRealMimoTranscript(
	t *testing.T,
	ctx context.Context,
	service session.Service,
	activeSession session.Session,
) {
	t.Helper()
	appendEvent := func(event *session.Event) {
		t.Helper()
		event.Visibility = session.VisibilityCanonical
		if _, err := service.AppendEvent(ctx, session.AppendEventRequest{SessionRef: activeSession.SessionRef, Event: event}); err != nil {
			t.Fatalf("append Guardian reproduction event: %v", err)
		}
	}
	userMessage := model.NewTextMessage(model.RoleUser, strings.Repeat(
		"Repair the repository task without changing the requested scope. Inspect evidence, preserve unrelated changes, and finish only after verification. ",
		12,
	))
	appendEvent(&session.Event{Type: session.EventTypeUser, Message: &userMessage})
	for index := 0; index < 13; index++ {
		assistantMessage := model.NewTextMessage(
			model.RoleAssistant,
			fmt.Sprintf("Attempt %d: I inspected the current state and will keep the command scoped to the requested repository operation.", index+1),
		)
		appendEvent(&session.Event{Type: session.EventTypeAssistant, Message: &assistantMessage})
		callID := fmt.Sprintf("terminal-call-%02d", index+1)
		appendEvent(&session.Event{
			Type: session.EventTypeToolCall,
			Tool: &session.EventTool{
				ID: callID, Name: "RUN_COMMAND", Status: "running",
				Input: map[string]any{"command": fmt.Sprintf("git status --short --untracked-files=no # attempt %d", index+1)},
			},
		})
		status := "completed"
		output := map[string]any{"exit_code": 0}
		if index%3 == 2 {
			status = "failed"
			output = map[string]any{
				"exit_code": 1,
				"error":     "sandbox backend rejected the operation before the repository command executed",
			}
		}
		appendEvent(&session.Event{
			Type: session.EventTypeToolResult,
			Tool: &session.EventTool{ID: callID, Name: "RUN_COMMAND", Status: status, Output: output},
		})
	}
}

func guardianRealMimoApprovalRequest(
	activeSession session.Session,
	llm model.LLM,
	repetition int,
) kernel.ApprovalReviewRequest {
	input := map[string]any{
		"command":             "git cherry-pick c499730ae050deb27e5d22972ea28daf778060f2",
		"sandbox_permissions": "require_escalated",
		"justification":       "The required repository operation failed in the sandbox; retry this exact command once on Host.",
	}
	raw, _ := json.Marshal(input)
	options := []kernel.ApprovalOption{
		{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
	}
	return kernel.ApprovalReviewRequest{
		SessionRef: activeSession.SessionRef,
		RunID:      "guardian-real-mimo-run",
		TurnID:     "guardian-real-mimo-turn",
		Mode:       kernel.ApprovalModeAutoReview,
		ReviewID:   fmt.Sprintf("guardian-real-mimo-review-%d", repetition),
		Model:      llm,
		Approval: &kernel.ApprovalPayload{
			ToolCallID:         "guardian-real-mimo-command",
			ToolName:           "RUN_COMMAND",
			ToolKind:           "execute",
			RawInput:           input,
			Reason:             "Execute the exact user-requested repository repair",
			Justification:      input["justification"].(string),
			SandboxPermissions: "require_escalated",
			Status:             kernel.ApprovalStatusPending,
			Options:            options,
		},
		RuntimeRequest: agent.ApprovalRequest{
			SessionRef: activeSession.SessionRef,
			Session:    activeSession,
			RunID:      "guardian-real-mimo-run",
			TurnID:     "guardian-real-mimo-turn",
			Tool:       tool.Definition{Name: "RUN_COMMAND"},
			Call:       tool.Call{ID: "guardian-real-mimo-command", Name: "RUN_COMMAND", Input: raw},
			Approval: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{
					ID: "guardian-real-mimo-command", Name: "RUN_COMMAND", Kind: "execute", Status: "pending", RawInput: input,
				},
				Options: []session.ProtocolApprovalOption{
					{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
					{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
				},
			},
		},
	}
}

type guardianDiagnosticObservation struct {
	Effort          string
	MaxOutputTokens int
	FinishReason    model.FinishReason
	RawFinishReason string
	Usage           model.Usage
	TextBytes       int
	ReasoningBytes  int
}

type guardianDiagnosticModel struct {
	inner model.LLM

	mu           sync.Mutex
	observations []guardianDiagnosticObservation
}

func (m *guardianDiagnosticModel) Name() string {
	if m == nil || m.inner == nil {
		return ""
	}
	return m.inner.Name()
}

func (m *guardianDiagnosticModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if m == nil || m.inner == nil {
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(nil, fmt.Errorf("Guardian diagnostic model is unavailable"))
		}
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		for event, err := range m.inner.Generate(ctx, req) {
			if event != nil && event.Response != nil {
				maxOutputTokens := 0
				effort := ""
				if req != nil {
					effort = strings.TrimSpace(req.Reasoning.Effort)
					if req.Output != nil {
						maxOutputTokens = req.Output.MaxOutputTokens
					}
				}
				m.mu.Lock()
				m.observations = append(m.observations, guardianDiagnosticObservation{
					Effort:          effort,
					MaxOutputTokens: maxOutputTokens,
					FinishReason:    event.FinishReason,
					RawFinishReason: event.RawFinishReason,
					Usage:           event.Usage,
					TextBytes:       len(event.Response.Message.TextContent()),
					ReasoningBytes:  len(event.Response.Message.ReasoningText()),
				})
				m.mu.Unlock()
			}
			if !yield(event, err) {
				return
			}
		}
	}
}

func (m *guardianDiagnosticModel) Capabilities() model.Capabilities {
	if m == nil || m.inner == nil {
		return model.Capabilities{}
	}
	capabilities, _ := model.CapabilitiesOf(m.inner)
	return capabilities
}

func (m *guardianDiagnosticModel) ProviderName() string {
	if m == nil || m.inner == nil {
		return ""
	}
	provider, _ := m.inner.(interface{ ProviderName() string })
	if provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.ProviderName())
}

func (m *guardianDiagnosticModel) ContextWindowTokens() int {
	if m == nil || m.inner == nil {
		return 0
	}
	provider, _ := m.inner.(interface{ ContextWindowTokens() int })
	if provider == nil {
		return 0
	}
	return provider.ContextWindowTokens()
}

func (m *guardianDiagnosticModel) snapshot() []guardianDiagnosticObservation {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]guardianDiagnosticObservation(nil), m.observations...)
}
