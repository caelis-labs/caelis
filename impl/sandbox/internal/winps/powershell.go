package winps

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

type Options struct {
	TTY         bool
	Interactive bool
}

func Args(command string, opts Options) []string {
	args := []string{"-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass"}
	if !opts.TTY && !opts.Interactive {
		args = append(args, "-NonInteractive")
	}
	return append(args, "-EncodedCommand", EncodedCommand(command))
}

func Command(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return utf8Prelude
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	return utf8Prelude +
		"$global:LASTEXITCODE = $null; " +
		"$__caelisUserCommand = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('" + encoded + "')); " +
		"try { $__caelisScriptBlock = [ScriptBlock]::Create($__caelisUserCommand); } " +
		"catch { $__caelisParseError = $_.Exception; if ($__caelisParseError.InnerException -ne $null) { $__caelisParseError = $__caelisParseError.InnerException; }; [Console]::Error.WriteLine($__caelisParseError.Message); exit 1; }; " +
		"$__caelisPropagateNativeExit = $false; " +
		"try { " +
		"$__caelisStatements = @(); " +
		"if ($__caelisScriptBlock.Ast.EndBlock -ne $null) { $__caelisStatements = @($__caelisScriptBlock.Ast.EndBlock.Statements); }; " +
		"if ($__caelisStatements.Count -gt 0) { " +
		"$__caelisLastStatement = $__caelisStatements[$__caelisStatements.Count - 1]; " +
		"if ($__caelisLastStatement -is [System.Management.Automation.Language.PipelineAst] -and $__caelisLastStatement.PipelineElements.Count -gt 0) { " +
		"$__caelisLastElement = $__caelisLastStatement.PipelineElements[$__caelisLastStatement.PipelineElements.Count - 1]; " +
		"if ($__caelisLastElement -is [System.Management.Automation.Language.CommandAst]) { " +
		"$__caelisLastCommandName = $__caelisLastElement.GetCommandName(); " +
		"if (-not [string]::IsNullOrWhiteSpace($__caelisLastCommandName)) { " +
		"$__caelisLastCommand = Get-Command -Name $__caelisLastCommandName -ErrorAction SilentlyContinue; " +
		"if ($__caelisLastCommand -ne $null -and $__caelisLastCommand.CommandType -eq [System.Management.Automation.CommandTypes]::Application) { $__caelisPropagateNativeExit = $true; }; " +
		"}; }; }; }; " +
		"} catch { $__caelisPropagateNativeExit = $false; }; " +
		"if ($__caelisPropagateNativeExit) { $__caelisScriptBlock = [ScriptBlock]::Create($__caelisUserCommand + \"`r`nif (`$global:LASTEXITCODE -is [int]) { exit `$global:LASTEXITCODE }\"); }; " +
		"& $__caelisScriptBlock; " +
		"$__caelisCommandSuccess = $?; " +
		"if (-not $__caelisCommandSuccess) { exit 1; }"
}

func EncodedCommand(command string) string {
	return base64.StdEncoding.EncodeToString(utf16LEBytes(Command(command)))
}

func utf16LEBytes(text string) []byte {
	words := utf16.Encode([]rune(text))
	out := make([]byte, len(words)*2)
	for i, word := range words {
		binary.LittleEndian.PutUint16(out[i*2:], word)
	}
	return out
}

const utf8Prelude = "" +
	"$__caelisUtf8Encoding = [System.Text.Encoding]::UTF8; " +
	"$OutputEncoding = $__caelisUtf8Encoding; " +
	"if ($ExecutionContext.SessionState.LanguageMode -ne 'ConstrainedLanguage') { " +
	"[Console]::InputEncoding = $__caelisUtf8Encoding; " +
	"[Console]::OutputEncoding = $__caelisUtf8Encoding; " +
	"$__caelisUtf8NoBomEncoding = [System.Text.UTF8Encoding]::new($false); " +
	"$__caelisUtf8OutWriter = [System.IO.StreamWriter]::new([Console]::OpenStandardOutput(), $__caelisUtf8NoBomEncoding); " +
	"$__caelisUtf8OutWriter.AutoFlush = $true; " +
	"[Console]::SetOut($__caelisUtf8OutWriter); " +
	"$__caelisUtf8ErrorWriter = [System.IO.StreamWriter]::new([Console]::OpenStandardError(), $__caelisUtf8NoBomEncoding); " +
	"$__caelisUtf8ErrorWriter.AutoFlush = $true; " +
	"[Console]::SetError($__caelisUtf8ErrorWriter); " +
	"}; "
