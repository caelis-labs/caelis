package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	runtimeacp "github.com/OnslaughtSnail/caelis/impl/agent/acp"
	bridgeassembly "github.com/OnslaughtSnail/caelis/impl/agent/acp/assembly"
	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	taskfile "github.com/OnslaughtSnail/caelis/impl/task/file"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	assemblyapi "github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
)

func main() {
	llm, err := resolveLLM()
	if err != nil {
		log.Fatal(err)
	}
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            sessionRootDir(),
		SessionIDGenerator: newSessionID,
	}))
	assembly, err := resolveAssembly()
	if err != nil {
		log.Fatal(err)
	}
	modeProvider, configProvider := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
		Assembly: assembly,
		Sessions: sessions,
		AppName:  "caelis",
		UserID:   "acp",
	})
	runtime, err := local.New(local.Config{
		Sessions: sessions,
		TaskStore: taskfile.NewStore(taskfile.Config{
			RootDir: taskRootDir(),
		}),
		Assembly: assembly,
		AgentFactory: chat.Factory{
			SystemPrompt: strings.TrimSpace(os.Getenv("SDK_ACP_SYSTEM_PROMPT")),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		AgentInfo: &acp.Implementation{
			Name:    "caelis-sdk",
			Title:   "Caelis SDK ACP Agent",
			Version: "0.1.0",
		},
		BuildAgentSpec: func(ctx context.Context, session session.Session, _ acp.PromptRequest) (agent.AgentSpec, error) {
			return buildSpec(ctx, session, llm, assembly)
		},
		Modes:  modeProvider,
		Config: configProvider,
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := acp.ServeStdio(context.Background(), agent, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func resolveLLM() (model.LLM, error) {
	if mode := strings.TrimSpace(os.Getenv("SDK_ACP_SCRIPTED_MODE")); mode != "" {
		switch mode {
		case "async_bash":
			return &scriptedAsyncBashLLM{}, nil
		case "approval_bash":
			return &scriptedApprovalBashLLM{}, nil
		case "probe_spawn":
			return &scriptedProbeSpawnLLM{}, nil
		case "spawn":
			return &scriptedSpawnLLM{}, nil
		case "spawn_passthrough":
			return &scriptedSpawnPassthroughLLM{}, nil
		case "mode_config":
			return &scriptedModeConfigLLM{}, nil
		default:
			return nil, fmt.Errorf("unknown SDK_ACP_SCRIPTED_MODE %q", mode)
		}
	}
	if reply := strings.TrimSpace(os.Getenv("SDK_ACP_STUB_REPLY")); reply != "" {
		delay := time.Duration(0)
		if raw := strings.TrimSpace(os.Getenv("SDK_ACP_STUB_DELAY_MS")); raw != "" {
			if ms, err := time.ParseDuration(raw + "ms"); err == nil {
				delay = ms
			}
		}
		return staticLLM{text: reply, delay: delay}, nil
	}
	return nil, fmt.Errorf("set SDK_ACP_SCRIPTED_MODE or SDK_ACP_STUB_REPLY for acpe2eagent")
}

func sessionRootDir() string {
	if root := strings.TrimSpace(os.Getenv("SDK_ACP_SESSION_ROOT")); root != "" {
		return root
	}
	return filepath.Join(os.TempDir(), "caelis-sdk-acp-sessions")
}

func taskRootDir() string {
	if root := strings.TrimSpace(os.Getenv("SDK_ACP_TASK_ROOT")); root != "" {
		return root
	}
	return filepath.Join(os.TempDir(), "caelis-sdk-acp-tasks")
}

func resolveAssembly() (assemblyapi.ResolvedAssembly, error) {
	assembly := assemblyapi.ResolvedAssembly{}

	if root := strings.TrimSpace(os.Getenv("SDK_ACP_SKILLS_ROOT")); root != "" {
		assembly.Skills = append(assembly.Skills, assemblyapi.SkillBundle{
			Plugin:    firstNonEmpty(strings.TrimSpace(os.Getenv("SDK_ACP_SKILLS_PLUGIN")), "app"),
			Namespace: strings.TrimSpace(os.Getenv("SDK_ACP_SKILLS_NAMESPACE")),
			Root:      root,
			Disabled:  splitCommaList(os.Getenv("SDK_ACP_SKILLS_DISABLED")),
		})
	}
	if strings.TrimSpace(os.Getenv("SDK_ACP_ENABLE_MODE_CONFIG")) == "1" {
		assembly.Modes = []assemblyapi.ModeConfig{
			{
				ID:          "default",
				Name:        "Default",
				Description: "Standard coding mode",
				Runtime: assemblyapi.RuntimeOverrides{
					PolicyMode:   "default",
					SystemPrompt: "mode-default-marker",
				},
			},
			{
				ID:          "plan",
				Name:        "Plan",
				Description: "Planning-first mode",
				Runtime: assemblyapi.RuntimeOverrides{
					PolicyMode:   "plan",
					SystemPrompt: "mode-plan-marker",
				},
			},
		}
		assembly.Configs = []assemblyapi.ConfigOption{{
			ID:           "reasoning",
			Name:         "Reasoning",
			Description:  "Reasoning profile",
			DefaultValue: "balanced",
			Options: []assemblyapi.ConfigSelectOption{
				{
					Value: "balanced",
					Name:  "Balanced",
					Runtime: assemblyapi.RuntimeOverrides{
						Reasoning: model.ReasoningConfig{Effort: "medium"},
					},
				},
				{
					Value: "deep",
					Name:  "Deep",
					Runtime: assemblyapi.RuntimeOverrides{
						Reasoning: model.ReasoningConfig{Effort: "high"},
					},
				},
			},
		}}
	}

	if strings.TrimSpace(os.Getenv("SDK_ACP_CHILD_NO_SPAWN")) == "1" {
		return assembly, nil
	}
	if strings.TrimSpace(os.Getenv("SDK_ACP_ENABLE_SPAWN")) != "1" {
		return assembly, nil
	}
	selfCmd := strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_CMD"))
	if selfCmd == "" {
		return assembly, nil
	}
	name := strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_NAME"))
	if name == "" {
		name = "self"
	}
	assembly.Agents = append(assembly.Agents, assemblyapi.AgentConfig{
		Name:        name,
		Description: strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_DESC")),
		Command:     "bash",
		Args:        []string{"-lc", selfCmd},
	})
	return assembly, nil
}

func newSessionID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return "session-" + hex.EncodeToString(buf[:])
}

type staticLLM struct {
	text  string
	delay time.Duration
}

func (m staticLLM) Name() string { return "static" }

func (m staticLLM) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.delay > 0 {
			time.Sleep(m.delay)
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, m.text),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

func buildSpec(_ context.Context, session session.Session, llm model.LLM, assembly assemblyapi.ResolvedAssembly) (agent.AgentSpec, error) {
	rt, err := host.New(host.Config{CWD: session.CWD})
	if err != nil {
		return agent.AgentSpec{}, err
	}
	tools, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: rt})
	if err != nil {
		return agent.AgentSpec{}, err
	}
	agents := make([]delegation.Agent, 0, len(assembly.Agents))
	for _, one := range assembly.Agents {
		agents = append(agents, delegation.Agent{
			Name:        strings.TrimSpace(one.Name),
			Description: strings.TrimSpace(one.Description),
		})
	}
	if len(agents) > 0 {
		tools = append(tools, spawn.New(agents))
	}
	return agent.AgentSpec{
		Name:  "chat",
		Model: llm,
		Tools: tools,
	}, nil
}

func splitCommaList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, one := range parts {
		one = strings.TrimSpace(one)
		if one != "" {
			out = append(out, one)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, one := range values {
		if strings.TrimSpace(one) != "" {
			return strings.TrimSpace(one)
		}
	}
	return ""
}

type scriptedAsyncBashLLM struct {
	calls  int
	taskID string
}

type scriptedSpawnLLM struct {
	calls  int
	taskID string
}

type scriptedApprovalBashLLM struct {
	calls  int
	taskID string
}

type scriptedProbeSpawnLLM struct{}
type scriptedSpawnPassthroughLLM struct {
	calls  int
	taskID string
}
type scriptedModeConfigLLM struct{}

func (m *scriptedSpawnLLM) Name() string { return "scripted-spawn" }

func (m *scriptedSpawnLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if m.calls == 2 {
		m.taskID = findTaskID(req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch m.calls {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-1",
						Name: spawn.ToolName,
						Args: string(mustJSON(map[string]any{
							"agent":  "self",
							"prompt": "Reply with exactly: spawn child ok",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-1",
						Name: "TASK",
						Args: string(mustJSON(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 300,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "spawn child ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *scriptedAsyncBashLLM) Name() string { return "scripted-async-bash" }

func (m *scriptedApprovalBashLLM) Name() string { return "scripted-approval-bash" }

func (m *scriptedProbeSpawnLLM) Name() string       { return "scripted-probe-spawn" }
func (m *scriptedSpawnPassthroughLLM) Name() string { return "scripted-spawn-passthrough" }
func (m *scriptedModeConfigLLM) Name() string       { return "scripted-mode-config" }

func (m *scriptedAsyncBashLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if m.calls == 2 {
		m.taskID = findTaskID(req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch m.calls {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "bash-async-1",
						Name: "BASH",
						Args: string(mustJSON(map[string]any{
							"command":       "sleep 0.05; printf 'acpx async bash ok'",
							"workdir":       ".",
							"yield_time_ms": 5,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-1",
						Name: "TASK",
						Args: string(mustJSON(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 300,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "acpx async bash ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *scriptedApprovalBashLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if m.calls == 2 {
		m.taskID = findTaskID(req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch m.calls {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "bash-approval-1",
						Name: "BASH",
						Args: string(mustJSON(map[string]any{
							"command":             "printf 'child approval ok'",
							"workdir":             ".",
							"yield_time_ms":       5,
							"sandbox_permissions": "require_escalated",
							"justification":       "Do you want to run this command outside the sandbox?",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-approval-1",
						Name: "TASK",
						Args: string(mustJSON(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 300,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "child approval ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *scriptedProbeSpawnLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	hasSpawn := false
	if req != nil {
		for _, one := range req.Tools {
			if one.Kind == model.ToolSpecKindFunction && one.Function != nil && strings.EqualFold(strings.TrimSpace(one.Function.Name), spawn.ToolName) {
				hasSpawn = true
				break
			}
		}
	}
	text := "spawn disabled"
	if hasSpawn {
		text = "spawn enabled"
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, text),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

func (m *scriptedSpawnPassthroughLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if m.calls == 2 {
		m.taskID = findTaskID(req)
	}
	resultText := strings.TrimSpace(findTaskResult(req))
	if resultText == "" {
		resultText = "spawn child ok"
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch m.calls {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-pass-1",
						Name: spawn.ToolName,
						Args: string(mustJSON(map[string]any{
							"agent":  "self",
							"prompt": "Check whether SPAWN is available and reply with exactly the result.",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-pass-1",
						Name: "TASK",
						Args: string(mustJSON(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 300,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, resultText),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *scriptedModeConfigLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	mode := "unknown"
	if req != nil {
		instructions := stringifyParts(req.Instructions)
		switch {
		case strings.Contains(instructions, "mode-plan-marker"):
			mode = "plan"
		case strings.Contains(instructions, "mode-default-marker"):
			mode = "default"
		}
	}
	effort := ""
	if req != nil {
		effort = strings.TrimSpace(req.Reasoning.Effort)
	}
	text := fmt.Sprintf("mode=%s effort=%s", mode, firstNonEmpty(effort, "none"))
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, text),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

func stringifyParts(parts []model.Part) string {
	var out []string
	for _, one := range parts {
		if one.Text != nil {
			if text := strings.TrimSpace(one.Text.Text); text != "" {
				out = append(out, text)
			}
			continue
		}
		if one.Reasoning != nil && one.Reasoning.VisibleText != nil {
			if text := strings.TrimSpace(*one.Reasoning.VisibleText); text != "" {
				out = append(out, text)
			}
		}
	}
	return strings.Join(out, "\n")
}

func findTaskID(req *model.Request) string {
	if req == nil {
		return ""
	}
	for _, message := range req.Messages {
		for _, result := range message.ToolResults() {
			for _, part := range result.Content {
				if part.Kind != model.PartKindJSON || part.JSON == nil {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal(part.JSONValue(), &payload); err != nil {
					continue
				}
				if taskID, _ := payload["task_id"].(string); strings.TrimSpace(taskID) != "" {
					return strings.TrimSpace(taskID)
				}
			}
		}
	}
	return ""
}

func findTaskResult(req *model.Request) string {
	if req == nil {
		return ""
	}
	for _, message := range req.Messages {
		for _, result := range message.ToolResults() {
			for _, part := range result.Content {
				if part.Kind != model.PartKindJSON || part.JSON == nil {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal(part.JSONValue(), &payload); err != nil {
					continue
				}
				if text, _ := payload["result"].(string); strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

func mustJSON(value map[string]any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
