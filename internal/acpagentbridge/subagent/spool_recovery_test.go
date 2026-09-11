package subagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	spoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
	"github.com/caelis-labs/caelis/control/taskstream"
)

type childReplayStore struct {
	task.Store
	entry *task.Entry
}

func (s childReplayStore) Get(context.Context, string) (*task.Entry, error) {
	return task.CloneEntry(s.entry), nil
}

type childReplayAccess struct{}

func (childReplayAccess) AuthorizeTaskStream(context.Context, taskstream.Principal, string) error {
	return nil
}
func (childReplayAccess) LoadSession(_ context.Context, req session.LoadSessionRequest) (session.LoadedSession, error) {
	return session.LoadedSession{Session: session.Session{SessionRef: req.SessionRef, CWD: os.TempDir()}}, nil
}

type childReplayCompletion chan delegation.Result

func (c childReplayCompletion) PublishSubagentCompletion(result delegation.Result) { c <- result }

func TestRestoredChildFollowsRealACPConnectionThroughOneSpool(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	release := filepath.Join(t.TempDir(), "finish")
	runner := newChildInputTestRunner(t, "spool-recovery", map[string]string{"CAELIS_ACP_CHILD_INPUT_AUTH_RELEASE": release}, controlagents.Authentication{})
	defer func() { _ = runner.Quiesce(context.Background()) }()
	spool, err := spoolfile.New(ctx, spoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	recorder := taskstream.NewRecorder(spool, nil)
	defer recorder.Close(context.Background())
	entry := &task.Entry{TaskID: "task-replay", Handle: "helper", Session: session.SessionRef{SessionID: "parent"}, Kind: task.KindSubagent, State: task.StateCompleted,
		Spec:     map[string]any{"target": delegation.AgentTarget("helper"), "agent_id": "child-agent"},
		Metadata: map[string]any{"session_id": "child-input-session", "child_activity_id": "old", "parent_call": "spawn"}}
	service, err := taskstream.New(taskstream.Config{Tasks: childReplayStore{entry: entry}, Spool: spool, Recorder: recorder, Sessions: childReplayAccess{}, SubagentHistory: runner, Authorizer: childReplayAccess{}, Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	request := taskstream.SubscribeRequest{SessionID: "parent", TaskID: entry.TaskID, Follow: true}
	result, err := service.Subscribe(ctx, taskstream.Principal{ID: "user"}, request)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	next := func() taskstream.Delivery {
		t.Helper()
		select {
		case d, ok := <-result.Subscription.Deliveries():
			if !ok {
				t.Fatalf("stream ended: %v", result.Subscription.Err())
			}
			return d
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return taskstream.Delivery{}
		}
	}
	var history []string
	for {
		d := next()
		for _, record := range d.Records {
			if record.Frame != nil && record.Frame.Event != nil {
				history = append(history, session.EventText(record.Frame.Event))
			}
		}
		if d.Kind == taskstream.DeliveryReplaceEnd {
			break
		}
	}
	if strings.Join(history, "|") != "original prompt|original reasoning|original answer" {
		t.Fatalf("history: %q", history)
	}
	anchor := delegation.Anchor{TaskID: entry.TaskID, SessionID: "child-input-session", AgentID: "child-agent"}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	originalClient := run.client
	completed := make(childReplayCompletion, 1)
	input, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{Target: run.slot.target, UserInput: true, Source: session.ActorRef{Kind: session.ActorKindUser, ID: "user"}, Input: "continue", ActivityID: "next",
		Output: recorder.BindTaskOutput(ctx, output.Binding{SessionID: "parent", TaskID: entry.TaskID, ActivityID: "next", Kind: output.TaskKindSubagent}), Completion: completed})
	if err != nil || !input.StartedActivity {
		t.Fatalf("input: %#v %v", input, err)
	}
	current, err := runner.lookup(anchor)
	if err != nil || current.client != originalClient {
		t.Fatal("follow-up replaced loaded ACP connection")
	}
	live := false
	for !live {
		d := next()
		for _, record := range d.Records {
			if record.Frame != nil && record.Frame.Event != nil && session.EventText(record.Frame.Event) == "live before completion" {
				live = true
				if record.Frame.Closed {
					t.Fatal("live output marked completed")
				}
			}
		}
	}
	select {
	case <-completed:
		t.Fatal("output only delivered after completion")
	default:
	}
	if err := os.WriteFile(release, []byte("finish"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	snapshot, err := service.Events(ctx, taskstream.Principal{ID: "user"}, taskstream.ReadRequest{SessionID: "parent", TaskID: entry.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, d := range snapshot.Deliveries {
		for _, record := range d.Records {
			if record.Frame != nil && record.Frame.Event != nil {
				all = append(all, session.EventText(record.Frame.Event))
			}
		}
	}
	if strings.Join(all, "|") != "original prompt|original reasoning|original answer|continue|live before completion|live final" {
		t.Fatalf("lost or duplicated transcript: %q", all)
	}
}
