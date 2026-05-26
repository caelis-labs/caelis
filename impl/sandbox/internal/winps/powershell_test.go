package winps

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestCommandGuardsConsoleEncodingInConstrainedLanguage(t *testing.T) {
	got := Command("Write-Output ok")
	if !strings.Contains(got, "LanguageMode -ne 'ConstrainedLanguage'") {
		t.Fatalf("Command() = %q, want constrained language guard around Console encoding setters", got)
	}
	if strings.Contains(got, "New-Object System.Text.UTF8Encoding") {
		t.Fatalf("Command() = %q, want Encoding.UTF8 without New-Object", got)
	}
	if !strings.Contains(got, "$OutputEncoding = $__caelisUtf8Encoding") {
		t.Fatalf("Command() = %q, want PowerShell output encoding assignment", got)
	}
	if !strings.Contains(got, "$ProgressPreference = 'SilentlyContinue'") {
		t.Fatalf("Command() = %q, want PowerShell progress stream silenced", got)
	}
	if !strings.Contains(got, "[Console]::SetError($__caelisUtf8ErrorWriter)") {
		t.Fatalf("Command() = %q, want PowerShell error stream UTF-8 assignment", got)
	}
	if !strings.Contains(got, "[Console]::SetOut($__caelisUtf8OutWriter)") {
		t.Fatalf("Command() = %q, want PowerShell output stream UTF-8 assignment", got)
	}
	if !strings.Contains(got, "[System.Text.UTF8Encoding]::new($false)") {
		t.Fatalf("Command() = %q, want no-BOM UTF-8 stream writers", got)
	}
	if !strings.Contains(got, "$__caelisScriptBlock = [ScriptBlock]::Create($__caelisUserCommand)") {
		t.Fatalf("Command() = %q, want user command parsed after stream setup", got)
	}
	if !strings.Contains(got, "[Console]::Error.WriteLine($__caelisParseError.Message)") {
		t.Fatalf("Command() = %q, want parser errors written through UTF-8 stderr", got)
	}
	if strings.Contains(got, "Write-Output ok") {
		t.Fatalf("Command() = %q, want user command carried as encoded text", got)
	}
}

func TestArgsUseEncodedCommand(t *testing.T) {
	args := Args("Write-Output ok", Options{})
	if len(args) < 2 || args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("Args() = %#v, want -EncodedCommand", args)
	}
	decoded, err := decodeUTF16LEBase64(args[len(args)-1])
	if err != nil {
		t.Fatalf("decode encoded command: %v", err)
	}
	if !strings.Contains(decoded, "$__caelisScriptBlock = [ScriptBlock]::Create($__caelisUserCommand)") ||
		!strings.Contains(decoded, "$OutputEncoding = $__caelisUtf8Encoding") {
		t.Fatalf("encoded command = %q, want UTF-8 wrapper script", decoded)
	}
	if !strings.Contains(decoded, "$global:LASTEXITCODE = $null") ||
		!strings.Contains(decoded, "$__caelisPropagateNativeExit") ||
		!strings.Contains(decoded, "exit `$global:LASTEXITCODE") {
		t.Fatalf("encoded command = %q, want native exit-code propagation", decoded)
	}
}

func TestCommandPropagatesFinalNativeExitCode(t *testing.T) {
	exitCode, stdout, stderr := runEncodedPowerShellCommand(t, "cmd.exe /d /c exit /b 7")
	if exitCode != 7 {
		t.Fatalf("exit code = %d stdout=%q stderr=%q, want 7", exitCode, stdout, stderr)
	}
}

func TestCommandDoesNotPropagateStaleNativeExitCode(t *testing.T) {
	exitCode, stdout, stderr := runEncodedPowerShellCommand(t, "cmd.exe /d /c exit /b 7; Write-Output ok")
	if exitCode != 0 {
		t.Fatalf("exit code = %d stdout=%q stderr=%q, want 0", exitCode, stdout, stderr)
	}
	if !strings.Contains(stdout, "ok") {
		t.Fatalf("stdout = %q, want ok", stdout)
	}
}

func TestCommandPreservesNativeStdoutNewlinesWithRedirect(t *testing.T) {
	exitCode, stdout, stderr := runEncodedPowerShellCommand(t, "python -c \"print('a'); print('b')\" 2>&1")
	if exitCode != 0 {
		t.Fatalf("exit code = %d stdout=%q stderr=%q, want 0", exitCode, stdout, stderr)
	}
	normalized := strings.ReplaceAll(stdout, "\r\n", "\n")
	if normalized != "a\nb\n" {
		t.Fatalf("stdout = %q, want native stdout line breaks preserved", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func runEncodedPowerShellCommand(t *testing.T, command string) (int, string, string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell exit-code behavior is Windows-specific")
	}
	exe, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skipf("powershell.exe unavailable: %v", err)
	}
	cmd := exec.Command(exe, Args(command, Options{})...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatalf("powershell.exe failed: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	return 0, "", ""
}

func decodeUTF16LEBase64(raw string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	words := make([]uint16, len(data)/2)
	for i := range words {
		words[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	return string(utf16.Decode(words)), nil
}
