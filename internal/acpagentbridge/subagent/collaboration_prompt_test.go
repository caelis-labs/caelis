package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

func TestWithCollaborationPromptSliceAppendsControlInstruction(t *testing.T) {
	t.Parallel()

	runner := &Runner{}
	prompt := acputil.BuildPromptParts("review the diff", nil)
	got := runner.withCollaborationPromptSlice(&childRun{
		spawn:            tasksubagent.SpawnContext{Handle: "@orbit", Role: session.ParticipantRoleSidecar},
		supportsSteering: false,
	}, prompt)
	if len(got) != 2 {
		t.Fatalf("len(prompt) = %d, want Control slice plus task", len(got))
	}
	raw := string(got[0]) + string(got[1])
	assertCollaborationSlice(t, raw, collaboration.PromptSlice{
		Handle: "orbit", Role: "sidecar",
	}, "review the diff")
	steering := runner.withCollaborationPromptSlice(&childRun{
		spawn:            tasksubagent.SpawnContext{Handle: "orbit", Role: session.ParticipantRoleDelegated},
		supportsSteering: true,
	}, prompt)
	if !strings.Contains(string(steering[1]), collaboration.CollaboratorInstructions()) {
		t.Fatalf("steering-capable slice omitted reporting guidance: %s", steering[1])
	}
}

func TestACPChildPromptInjectsIdentityAndReportingWithoutSteering(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	trace := t.TempDir() + "/prompt.trace"
	runner := collaborationPromptTestRunner(t, "complete", `{"supported":false}`, trace)
	events := make(chan childInputTestEvent, 8)
	spawn := childInputSpawnContext(t, "task-collab-initial", events)
	spawn.Handle = "orbit"
	spawn.Role = session.ParticipantRoleSidecar
	_, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "review the diff"})
	if err != nil {
		t.Fatal(err)
	}
	frames, terminal := waitSpawnInputProjectionFrames(t, ctx, events, spawn.ActivityID)
	if terminal.Result == nil || terminal.Result.State != delegation.StateCompleted {
		t.Fatalf("terminal = %#v, want completed", terminal.Result)
	}
	input := frames[0].Event
	if input.Actor.Kind != session.ActorKindUser || input.Text != "review the diff" || session.ProtocolAgentCommunicationOf(input) != nil {
		t.Fatalf("display input = %#v, want original task prose", frames[0].Event)
	}
	if strings.Contains(input.Text, collaboration.SliceOpenTag) || strings.Contains(input.Text, "ReceiveMessages") {
		t.Fatalf("Control slice leaked into display projection: %#v", frames[0].Event)
	}
	payloads := readCollaborationPromptTrace(t, trace)
	if len(payloads) != 1 || payloads[0].Method != client.MethodSessionPrompt {
		t.Fatalf("trace = %#v, want one session/prompt", payloads)
	}
	assertCollaborationSlice(t, payloads[0].Raw, collaboration.PromptSlice{
		Handle: "orbit", Role: "sidecar",
	}, "review the diff")
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestACPChildIdleFollowupOmitsIdentitySlice(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	trace := t.TempDir() + "/prompt.trace"
	runner := collaborationPromptTestRunner(t, "complete", `{"supported":false}`, trace)
	events := make(chan childInputTestEvent, 8)
	spawn := childInputSpawnContext(t, "task-collab-followup", events)
	spawn.Handle = "orbit"
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
		Target: run.slot.target, Source: session.ParentCommunicationActor(), Input: "continue",
	}); err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	payloads := readCollaborationPromptTrace(t, trace)
	if len(payloads) != 2 {
		t.Fatalf("trace count = %d, want initial plus followup: %#v", len(payloads), payloads)
	}
	for i, payload := range payloads {
		if payload.Method != client.MethodSessionPrompt {
			t.Fatalf("payload[%d] method = %q, want session/prompt", i, payload.Method)
		}
		if i == 0 {
			assertCollaborationSlice(t, payload.Raw, collaboration.PromptSlice{
				Handle: "orbit", Role: "delegated",
			}, "first")
		} else if strings.Contains(payload.Raw, "caelis_collaboration") || strings.Contains(payload.Raw, "assigned handle") {
			t.Fatalf("followup repeated bootstrap: %s", payload.Raw)
		}
	}
	if !strings.Contains(payloads[1].Raw, "continue") {
		t.Fatalf("followup prompt missing task text: %s", payloads[1].Raw)
	}
	if strings.Contains(payloads[1].Raw, "CAELIS_COLLABORATION_TOKEN") {
		t.Fatalf("followup prompt leaked secret: %s", payloads[1].Raw)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestACPChildSteeringOmitsCollaborationSlice(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	trace := t.TempDir() + "/prompt.trace"
	runner := collaborationPromptTestRunner(t, "hold", `{"supported":true}`, trace)
	events := make(chan childInputTestEvent, 8)
	spawn := childInputSpawnContext(t, "task-collab-steer", events)
	spawn.Handle = "orbit"
	anchor, initial, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "first"})
	if err != nil || !initial.Running {
		t.Fatalf("Spawn() = (%#v, %v), want running", initial, err)
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
		Target: run.slot.target, Source: session.ParentCommunicationActor(), Input: "steer now",
	}); err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	payloads := readCollaborationPromptTrace(t, trace)
	promptRaw, steeringRaw := "", ""
	for _, payload := range payloads {
		switch payload.Method {
		case client.MethodSessionPrompt:
			promptRaw = payload.Raw
		case client.MethodSessionSteering:
			steeringRaw = payload.Raw
		}
	}
	if promptRaw == "" || steeringRaw == "" {
		t.Fatalf("trace = %#v, want session/prompt and steering", payloads)
	}
	assertCollaborationSlice(t, promptRaw, collaboration.PromptSlice{
		Handle: "orbit", Role: "delegated",
	}, "first")
	if !strings.Contains(promptRaw, collaboration.CollaboratorInstructions()) {
		t.Fatalf("steering-capable initial prompt omitted reporting guidance: %s", promptRaw)
	}
	if strings.Contains(steeringRaw, "caelis_collaboration") || strings.Contains(steeringRaw, "ReceiveMessages") || strings.Contains(steeringRaw, collaboration.DiscoveryInstruction()) {
		t.Fatalf("steering mixed Control slice into a peer message: %s", steeringRaw)
	}
	if !strings.Contains(steeringRaw, "steer now") {
		t.Fatalf("steering missing peer text: %s", steeringRaw)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

type collaborationPromptTrace struct {
	Method string
	Raw    string
}

func collaborationPromptTestRunner(t *testing.T, mode, steeringMeta, trace string) *Runner {
	t.Helper()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestCollaborationPromptHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_COLLAB_PROMPT_HELPER": "1",
			"CAELIS_ACP_COLLAB_PROMPT_MODE":   mode,
			"CAELIS_ACP_COLLAB_PROMPT_META":   steeringMeta,
			"CAELIS_ACP_COLLAB_PROMPT_TRACE":  trace,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func readCollaborationPromptTrace(t *testing.T, path string) []collaborationPromptTrace {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []collaborationPromptTrace
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		method, payload, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("trace line %q missing payload", line)
		}
		out = append(out, collaborationPromptTrace{Method: method, Raw: payload})
	}
	return out
}

func assertCollaborationSlice(t *testing.T, raw string, slice collaboration.PromptSlice, task string) {
	t.Helper()
	identity := collaboration.IdentityInstructions(slice.Handle, slice.Role)
	if !strings.Contains(raw, "caelis_collaboration") || !strings.Contains(raw, identity) {
		t.Fatalf("ACP prompt missing Control identity slice %q: %s", identity, raw)
	}
	if !strings.Contains(raw, "caelis-collaboration") {
		t.Fatalf("ACP prompt missing stable discovery key: %s", raw)
	}
	if !strings.Contains(raw, collaboration.CollaboratorInstructions()) {
		t.Fatalf("ACP prompt omitted reporting guidance: %s", raw)
	}
	if strings.Contains(raw, "CAELIS_COLLABORATION_TOKEN") || strings.Contains(raw, "Bearer") {
		t.Fatalf("ACP prompt leaked secret: %s", raw)
	}
	if task != "" && !strings.Contains(raw, task) {
		t.Fatalf("ACP prompt missing task text %q: %s", task, raw)
	}
	if task != "" && strings.Index(raw, task) > strings.Index(raw, "caelis_collaboration") {
		t.Fatalf("Control slice preceded task prose: %s", raw)
	}
}

func TestCollaborationPromptHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_COLLAB_PROMPT_HELPER") != "1" {
		return
	}
	mode := strings.TrimSpace(os.Getenv("CAELIS_ACP_COLLAB_PROMPT_MODE"))
	trace := os.Getenv("CAELIS_ACP_COLLAB_PROMPT_TRACE")
	steered := make(chan struct{})
	var recordMu sync.Mutex
	record := func(method string, params json.RawMessage) {
		recordMu.Lock()
		defer recordMu.Unlock()
		f, err := os.OpenFile(trace, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(f, "%s\t%s\n", method, strings.ReplaceAll(string(params), "\n", " "))
		_ = f.Close()
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, message jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch message.Method {
		case client.MethodInitialize:
			return client.InitializeResponse{
				ProtocolVersion: 1,
				Meta: map[string]json.RawMessage{
					client.SessionSteeringMetaKey: json.RawMessage(os.Getenv("CAELIS_ACP_COLLAB_PROMPT_META")),
				},
			}, nil
		case client.MethodSessionNew:
			return client.NewSessionResponse{SessionID: "collab-prompt-session"}, nil
		case client.MethodSessionResume:
			return client.ResumeSessionResponse{}, nil
		case client.MethodSessionPrompt:
			record(message.Method, message.Params)
			if mode == "hold" {
				select {
				case <-steered:
				case <-time.After(4 * time.Second):
					return nil, &jsonrpc.RPCError{Code: -32000, Message: "steering timeout"}
				}
			}
			return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
		case client.MethodSessionSteering:
			record(message.Method, message.Params)
			select {
			case <-steered:
			default:
				close(steered)
			}
			return client.SessionSteeringResponse{Outcome: client.SessionSteeringInjected}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
