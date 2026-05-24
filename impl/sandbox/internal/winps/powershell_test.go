package winps

import (
	"encoding/base64"
	"encoding/binary"
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
