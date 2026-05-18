//go:build windows

package runnercmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/runnerproto"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
)

func TestRunnerCommandEmitsOutputAndExit(t *testing.T) {
	var input bytes.Buffer
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       "Write-Output runner-ok",
		CapabilitySID: testCapabilitySIDs(t),
	})
	if err != nil {
		t.Fatalf("NewFrame() error = %v", err)
	}
	if err := runnerproto.NewWriter(&input).WriteFrame(spawn); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}

	var output bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(&input, &output, &stderr); code != 0 {
		t.Fatalf("Run() code = %d stderr=%s", code, stderr.String())
	}

	reader := runnerproto.NewReader(&output)
	hello, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	if hello.Type != runnerproto.TypeHello {
		t.Fatalf("hello type = %q", hello.Type)
	}
	var helloPayload runnerproto.Hello
	if err := hello.DecodePayload(&helloPayload); err != nil {
		t.Fatalf("DecodePayload(hello) error = %v", err)
	}
	if !containsString(helloPayload.Capabilities, "capability_restricted_sid") {
		t.Fatalf("hello capabilities = %#v, want capability restricted SID support", helloPayload.Capabilities)
	}
	var sawOutput bool
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		switch frame.Type {
		case runnerproto.TypeStdout:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(stdout) error = %v", err)
			}
			if bytes.Contains(payload.Data, []byte("runner-ok")) {
				sawOutput = true
			}
		case runnerproto.TypeExit:
			var payload runnerproto.Exit
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(exit) error = %v", err)
			}
			if payload.ExitCode != 0 {
				t.Fatalf("exit = %+v", payload)
			}
			if !sawOutput {
				t.Fatal("runner exited before stdout was observed")
			}
			return
		}
	}
}

func TestRunnerCommandUsesUTF8AndReadHostStdin(t *testing.T) {
	var input bytes.Buffer
	writer := runnerproto.NewWriter(&input)
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       "$name = Read-Host '请输入你的名字'; Write-Host ('你好，' + $name + '！')",
		StdinOpen:     true,
		Timeout:       10 * time.Second,
		CapabilitySID: testCapabilitySIDs(t),
	})
	if err != nil {
		t.Fatalf("NewFrame(spawn) error = %v", err)
	}
	if err := writer.WriteFrame(spawn); err != nil {
		t.Fatalf("WriteFrame(spawn) error = %v", err)
	}
	stdin, err := runnerproto.NewFrame(runnerproto.TypeStdin, runnerproto.Bytes{Data: []byte("世界\n")})
	if err != nil {
		t.Fatalf("NewFrame(stdin) error = %v", err)
	}
	if err := writer.WriteFrame(stdin); err != nil {
		t.Fatalf("WriteFrame(stdin) error = %v", err)
	}
	closeStdin, err := runnerproto.NewFrame(runnerproto.TypeStdinClose, nil)
	if err != nil {
		t.Fatalf("NewFrame(stdin close) error = %v", err)
	}
	if err := writer.WriteFrame(closeStdin); err != nil {
		t.Fatalf("WriteFrame(stdin close) error = %v", err)
	}

	var output bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(&input, &output, &stderr); code != 0 {
		t.Fatalf("Run() code = %d stderr=%s", code, stderr.String())
	}

	stdout, stderrText, exit := readRunnerOutputForTest(t, &output)
	if exit.ExitCode != 0 {
		t.Fatalf("exit = %+v stdout=%q stderr=%q", exit, stdout, stderrText)
	}
	if !strings.Contains(stdout, "你好，世界！") {
		t.Fatalf("stdout = %q, want UTF-8 greeting", stdout)
	}
	if strings.Contains(stdout, "\ufffd") || strings.Contains(stderrText, "\ufffd") {
		t.Fatalf("runner output contains replacement characters: stdout=%q stderr=%q", stdout, stderrText)
	}
}

func TestMergeEnvNetworkModes(t *testing.T) {
	env, err := mergeEnv(map[string]string{"EXTRA": "1"}, "offline", t.TempDir())
	if err != nil {
		t.Fatalf("mergeEnv(offline) error = %v", err)
	}
	offline := strings.Join(env, "\n")
	for _, want := range []string{
		"CAELIS_SANDBOX_HOME=",
		"USERPROFILE=",
		"TEMP=",
		"CAELIS_SANDBOX_NETWORK=disabled",
		"HTTP_PROXY=http://127.0.0.1:9",
		"HTTPS_PROXY=http://127.0.0.1:9",
		"ALL_PROXY=http://127.0.0.1:9",
		"NO_PROXY=localhost,127.0.0.1,::1",
		"EXTRA=1",
	} {
		if !strings.Contains(offline, want) {
			t.Fatalf("offline env missing %q in %q", want, offline)
		}
	}

	env, err = mergeEnv(nil, "online", "")
	if err != nil {
		t.Fatalf("mergeEnv(online) error = %v", err)
	}
	online := strings.Join(env, "\n")
	if strings.Contains(online, "CAELIS_SANDBOX_NETWORK=disabled") ||
		strings.Contains(online, "HTTP_PROXY=http://127.0.0.1:9") {
		t.Fatalf("online env = %q, want no offline proxy hardening", online)
	}
}

func readRunnerOutputForTest(t *testing.T, output *bytes.Buffer) (string, string, runnerproto.Exit) {
	t.Helper()
	reader := runnerproto.NewReader(output)
	hello, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	if hello.Type != runnerproto.TypeHello {
		t.Fatalf("hello type = %q", hello.Type)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		switch frame.Type {
		case runnerproto.TypeStdout:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(stdout) error = %v", err)
			}
			stdout.Write(payload.Data)
		case runnerproto.TypeStderr:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(stderr) error = %v", err)
			}
			stderr.Write(payload.Data)
		case runnerproto.TypeExit:
			var payload runnerproto.Exit
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(exit) error = %v", err)
			}
			return stdout.String(), stderr.String(), payload
		}
	}
}

func TestRunnerTTYCommandUsesConPTY(t *testing.T) {
	var input bytes.Buffer
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       "Write-Output tty-ok",
		TTY:           true,
		Rows:          24,
		Cols:          80,
		Timeout:       10 * time.Second,
		CapabilitySID: testCapabilitySIDs(t),
	})
	if err != nil {
		t.Fatalf("NewFrame() error = %v", err)
	}
	if err := runnerproto.NewWriter(&input).WriteFrame(spawn); err != nil {
		t.Fatalf("WriteFrame(spawn) error = %v", err)
	}
	resize, err := runnerproto.NewFrame(runnerproto.TypeResize, runnerproto.Resize{Rows: 30, Cols: 100})
	if err != nil {
		t.Fatalf("NewFrame(resize) error = %v", err)
	}
	if err := runnerproto.NewWriter(&input).WriteFrame(resize); err != nil {
		t.Fatalf("WriteFrame(resize) error = %v", err)
	}

	var output bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(&input, &output, &stderr); code != 0 {
		if strings.Contains(stderr.String(), "CreatePseudoConsole") {
			t.Skipf("ConPTY is unavailable in this Windows environment: %s", stderr.String())
		}
		t.Fatalf("Run() code = %d stderr=%s", code, stderr.String())
	}

	reader := runnerproto.NewReader(&output)
	hello, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	var helloPayload runnerproto.Hello
	if err := hello.DecodePayload(&helloPayload); err != nil {
		t.Fatalf("DecodePayload(hello) error = %v", err)
	}
	if !containsString(helloPayload.Capabilities, "conpty") || !containsString(helloPayload.Capabilities, "resize") {
		t.Fatalf("hello capabilities = %#v, want conpty and resize", helloPayload.Capabilities)
	}
	var stdout bytes.Buffer
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		switch frame.Type {
		case runnerproto.TypeStdout:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(stdout) error = %v", err)
			}
			stdout.Write(payload.Data)
		case runnerproto.TypeExit:
			var payload runnerproto.Exit
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(exit) error = %v", err)
			}
			if payload.ExitCode != 0 {
				t.Fatalf("exit = %+v stdout=%q", payload, stdout.String())
			}
			if !strings.Contains(stdout.String(), "tty-ok") {
				t.Fatalf("stdout = %q, want tty-ok", stdout.String())
			}
			return
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testCapabilitySIDs(t *testing.T) []string {
	t.Helper()
	sids, err := win32.DeriveCapabilitySIDs("internetClient")
	if err != nil {
		t.Fatalf("DeriveCapabilitySIDs() error = %v", err)
	}
	if len(sids.Group) == 0 {
		t.Fatalf("DeriveCapabilitySIDs() = %#v, want capability group SID", sids)
	}
	return sids.Group
}
