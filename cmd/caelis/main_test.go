package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestRunMainUsesInternalHelperBeforeCLI(t *testing.T) {
	calledCLI := false
	code := runMain(
		context.Background(),
		[]string{"internal-helper"},
		nil,
		io.Discard,
		io.Discard,
		func(args []string) bool {
			return reflect.DeepEqual(args, []string{"internal-helper"})
		},
		func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
			calledCLI = true
			return nil
		},
	)
	if code != 0 {
		t.Fatalf("runMain() code = %d, want 0", code)
	}
	if calledCLI {
		t.Fatal("CLI runner was called after internal helper handled the invocation")
	}
}

func TestRunMainReportsCLIError(t *testing.T) {
	var stderr bytes.Buffer
	code := runMain(
		context.Background(),
		[]string{"--bad"},
		nil,
		io.Discard,
		&stderr,
		func([]string) bool { return false },
		func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
			return errors.New("boom")
		},
	)
	if code != 1 {
		t.Fatalf("runMain() code = %d, want 1", code)
	}
	if stderr.String() != "boom\n" {
		t.Fatalf("stderr = %q, want boom newline", stderr.String())
	}
}

// The installed-entry check uses an explicitly built CLI; ordinary unit tests
// do not recursively build the product binary.
func TestCodeModeCLIWorkerHandshake(t *testing.T) {
	binaryPath := os.Getenv("CAELIS_TEST_CLI_BINARY")
	if binaryPath == "" {
		t.Skip("set CAELIS_TEST_CLI_BINARY to a native Core CLI build")
	}
	binaryPath, err := filepath.Abs(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, gojaexec.WorkerArgument)
	cmd.Env = []string{"GOMAXPROCS=1"}
	var input, stderr bytes.Buffer
	send := func(kind byte, payload string) {
		var header [13]byte
		binary.BigEndian.PutUint32(header[:4], uint32(len(payload)))
		header[4] = kind
		input.Write(header[:])
		input.WriteString(payload)
	}
	send(1, `{"limits":{}}`)
	send(10, `[{"name":"NativeFixture"}]`)
	send(10, "") // End the name snapshot before sending source.
	send(2, `return {text:"native-cli",total:42,fs:typeof require,output:typeof output,describe:typeof tools.describe,direct:typeof NativeFixture,native:typeof tools.native.NativeFixture};`)
	send(11, "") // Persistent workers exit only after the explicit close frame.
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err = stdin.Write(input.Bytes()); err != nil {
		_ = cmd.Wait()
		t.Fatal(err)
	}
	err = cmd.Wait()
	raw := stdout.Bytes()
	if err != nil {
		t.Fatalf("early worker handshake: %v stderr=%q", err, stderr.String())
	}
	next := func() (byte, []byte) {
		if len(raw) < 13 {
			t.Fatalf("truncated protocol: %q", raw)
		}
		n := int(binary.BigEndian.Uint32(raw[:4]))
		kind := raw[4]
		if n > len(raw)-13 {
			t.Fatalf("truncated payload")
		}
		payload := raw[13 : 13+n]
		raw = raw[13+n:]
		return kind, payload
	}
	kind, _ := next()
	if kind != 3 {
		t.Fatalf("worker ready=%d", kind)
	}
	kind, payload := next()
	if kind != 6 {
		t.Fatalf("worker result=%d %s", kind, payload)
	}
	var output struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(payload, &output) != nil {
		t.Fatal("invalid worker output envelope")
	}
	var result struct {
		Text     string `json:"text"`
		Total    int    `json:"total"`
		FS       string `json:"fs"`
		Output   string `json:"output"`
		Describe string `json:"describe"`
		Direct   string `json:"direct"`
		Native   string `json:"native"`
	}
	if json.Unmarshal(output.Value, &result) != nil || result.Total != 42 || result.FS != "undefined" || result.Text != "native-cli" || result.Output != "undefined" || result.Describe != "undefined" || result.Direct != "function" || result.Native != "function" || len(raw) != 0 {
		t.Fatalf("worker protocol result=%s trailing=%d", payload, len(raw))
	}
	t.Logf("native CLI early-worker handshake complete; process Wait succeeded, result=%s", payload)
}

func TestCodeModeCLIWorkerCancellationReapsProcess(t *testing.T) {
	binaryPath := os.Getenv("CAELIS_TEST_CLI_BINARY")
	if binaryPath == "" {
		t.Skip("set CAELIS_TEST_CLI_BINARY to a native Core CLI build")
	}
	binaryPath, err := filepath.Abs(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, gojaexec.WorkerArgument)
	cmd.Env = []string{"GOMAXPROCS=1"}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	for _, frame := range []struct {
		kind    byte
		payload string
	}{
		{1, `{"limits":{}}`},
		{10, `[]`},
		{10, ""},
		{2, `while(true){}`},
	} {
		var header [13]byte
		binary.BigEndian.PutUint32(header[:4], uint32(len(frame.payload)))
		header[4] = frame.kind
		input.Write(header[:])
		input.WriteString(frame.payload)
	}
	if _, err = stdin.Write(input.Bytes()); err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	var ready [13]byte
	if _, err = io.ReadFull(stdout, ready[:]); err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("ready: %v stderr=%s", err, stderr.String())
	}
	if ready[4] != 3 || binary.BigEndian.Uint32(ready[:4]) != 0 {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("unexpected ready frame: %v", ready)
	}
	cancel()
	if err = cmd.Wait(); err == nil || cmd.ProcessState == nil {
		t.Fatalf("cancel did not terminate and reap native CLI: %v", err)
	}
	t.Logf("native CLI cancellation reaped pid=%d", cmd.Process.Pid)
}
