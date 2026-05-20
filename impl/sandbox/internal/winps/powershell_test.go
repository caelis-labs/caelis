package winps

import (
	"strings"
	"testing"
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
}
