package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestRuntimeStartAndReopenSession(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	session, err := rt.Start(context.Background(), commandRequest("printf 'hello'; sleep 0.1; printf ' world'"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := session.Terminal().TerminalID; got == "" {
		t.Fatal("TerminalID = empty, want stable terminal anchor")
	}
	reopened, err := rt.OpenSession(session.Ref().SessionID)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	status, err := reopened.Wait(context.Background(), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running {
		t.Fatalf("status.Running = true, want false")
	}
	result, err := reopened.Result(context.Background())
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got := result.Stdout; !strings.Contains(got, "hello world") {
		t.Fatalf("stdout = %q, want hello world", got)
	}
}

func TestRuntimeSessionWriteInput(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	session, err := rt.Start(context.Background(), commandRequest("read line; printf 'got:%s' \"$line\""))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := session.WriteInput(context.Background(), []byte("demo\n")); err != nil {
		t.Fatalf("WriteInput() error = %v", err)
	}
	status, err := session.Wait(context.Background(), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running {
		t.Fatalf("status.Running = true, want false")
	}
	result, err := session.Result(context.Background())
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got := result.Stdout; !strings.Contains(got, "got:demo") {
		t.Fatalf("stdout = %q, want got:demo", got)
	}
}

func TestRuntimeSessionReadOutputWithCursor(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	session, err := rt.Start(context.Background(), commandRequest("printf 'one'; sleep 0.05; printf 'two'"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	_, cursor1 := waitForStdoutContains(t, session, 0, "one")
	_, err = session.Wait(context.Background(), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	stdout2, _, _, _, err := session.ReadOutput(context.Background(), cursor1, 0)
	if err != nil {
		t.Fatalf("ReadOutput(cursor1,0) error = %v", err)
	}
	if got := string(stdout2); !strings.Contains(got, "two") {
		t.Fatalf("stdout2 = %q, want two", got)
	}
}

func TestRuntimeWaitDrainsOutputBeforeCompletion(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	session, err := rt.Start(context.Background(), commandRequest("i=0; while [ $i -lt 2000 ]; do printf 'line-%04d\\n' \"$i\"; i=$((i+1)); done"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	status, err := session.Wait(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running {
		t.Fatalf("status.Running = true, want false")
	}
	result, err := session.Result(context.Background())
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got := result.Stdout; !strings.Contains(got, "line-0000\n") || !strings.Contains(got, "line-1999\n") {
		t.Fatalf("stdout missing drained output, len=%d tail=%q", len(got), tailString(got, 80))
	}
}

func waitForStdoutContains(t *testing.T, session sandbox.Session, marker int64, want string) ([]byte, int64) {
	t.Helper()

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		stdout, _, cursor, _, err := session.ReadOutput(context.Background(), marker, 0)
		if err != nil {
			t.Fatalf("ReadOutput(%d,0) error = %v", marker, err)
		}
		if strings.Contains(string(stdout), want) {
			return stdout, cursor
		}
		if time.Now().After(deadline) {
			t.Fatalf("stdout = %q, want %s", string(stdout), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func tailString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func commandRequest(command string) sandbox.CommandRequest {
	return sandbox.CommandRequest{Command: command}
}
