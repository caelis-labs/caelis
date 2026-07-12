//go:build darwin

package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/seatbelt"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestSeatbeltTerminalSubscribePreservesCompletionTailDuringTaskWait(t *testing.T) {
	root := t.TempDir()
	workdir := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-seatbelt-terminal-tail" },
	})
	sessions := sessionfile.NewService(store)
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	agentRuntime, err := New(Config{
		Sessions:     sessions,
		TaskStore:    sessionfile.NewTaskStore(store),
		AgentFactory: chat.Factory{SystemPrompt: "Use tools when necessary."},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	backend, err := seatbelt.New(sandbox.Config{CWD: workdir})
	if err != nil {
		t.Fatalf("seatbelt.New() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	agentRuntime.tasks.registerSandboxRuntime(backend)

	const want = "[Step 1/5] 正在处理...\n[Step 2/5] 正在处理...\n[Step 3/5] 正在处理...\n[Step 4/5] 正在处理...\n[Step 5/5] 正在处理...\n✅ 全部完成！\n"
	snapshot, err := agentRuntime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command:    `for i in $(seq 1 5); do echo "[Step $i/5] 正在处理..."; sleep 0.1; done; echo "✅ 全部完成！"`,
		Workdir:    workdir,
		Yield:      150 * time.Millisecond,
		ParentCall: "call-seatbelt-terminal-tail",
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if !snapshot.Running {
		t.Fatalf("StartCommand() snapshot = %#v, want running", snapshot)
	}
	initial := taskRawStringValue(snapshot.Result["latest_output"])
	cursor, _ := taskInt64Value(snapshot.Metadata["output_cursor"])

	type streamResult struct {
		text   string
		frames []stream.Frame
		err    error
	}
	streamCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan streamResult, 1)
	go func() {
		var result streamResult
		var text strings.Builder
		for frame, streamErr := range agentRuntime.Streams().Subscribe(streamCtx, stream.SubscribeRequest{
			Ref:    stream.Ref{SessionID: activeSession.SessionID, TaskID: snapshot.Ref.TaskID},
			Cursor: stream.Cursor{Output: cursor},
		}) {
			if streamErr != nil {
				result.err = streamErr
				break
			}
			if frame != nil {
				text.WriteString(frame.Text)
				result.frames = append(result.frames, stream.CloneFrame(*frame))
			}
		}
		result.text = text.String()
		done <- result
	}()

	waited, err := agentRuntime.tasks.Wait(context.Background(), activeSession.SessionRef, taskapi.ControlRequest{
		TaskID:    snapshot.Ref.TaskID,
		Yield:     5 * time.Second,
		Principal: session.ActorKindController,
	})
	if err != nil {
		t.Fatalf("TASK wait error = %v", err)
	}
	streamed := <-done
	if streamed.err != nil {
		t.Fatalf("terminal subscription error = %v", streamed.err)
	}
	if got := initial + streamed.text; got != want {
		t.Fatalf("initial + streamed output = %q, want %q; TASK result = %q; frames = %#v", got, want, taskRawStringValue(waited.Result["result"]), streamed.frames)
	}
}
