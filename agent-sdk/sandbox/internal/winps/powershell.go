package winps

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

type Options struct {
	TTY               bool
	Interactive       bool
	SkipModulePrelude bool
}

// ExecArgs returns powershell.exe arguments without reordering PSModulePath.
func ExecArgs(command string) []string {
	return Args(command, Options{SkipModulePrelude: true})
}

func Args(command string, opts Options) []string {
	args := []string{"-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass"}
	if !opts.TTY && !opts.Interactive {
		args = append(args, "-NonInteractive")
	}
	return append(args, "-EncodedCommand", encodedCommandWithOptions(command, opts))
}

func Command(command string) string {
	return CommandWithOptions(command, Options{})
}

func CommandWithOptions(command string, opts Options) string {
	prelude := builtinModulePrelude + utf8Prelude
	if opts.SkipModulePrelude {
		prelude = utf8Prelude
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return prelude
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	return prelude +
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
		"$__caelisStatusSuffix = \"`r`nif (-not `$?) { exit 1 }\"; " +
		"if ($__caelisPropagateNativeExit) { $__caelisStatusSuffix = \"`r`nif (`$global:LASTEXITCODE -is [int]) { exit `$global:LASTEXITCODE }`r`nif (-not `$?) { exit 1 }\"; }; " +
		"$__caelisScriptBlock = [ScriptBlock]::Create($__caelisUserCommand + $__caelisStatusSuffix); " +
		"& $__caelisScriptBlock; " +
		"$__caelisCommandSuccess = $?; " +
		"if (-not $__caelisCommandSuccess) { exit 1; }"
}

func EncodedCommand(command string) string {
	return encodedCommandWithOptions(command, Options{})
}

func encodedCommandWithOptions(command string, opts Options) string {
	return base64.StdEncoding.EncodeToString(utf16LEBytes(CommandWithOptions(command, opts)))
}

func utf16LEBytes(text string) []byte {
	words := utf16.Encode([]rune(text))
	out := make([]byte, len(words)*2)
	for i, word := range words {
		binary.LittleEndian.PutUint16(out[i*2:], word)
	}
	return out
}

// PowerShell can prepend machine module paths during startup. Reorder after
// startup, before Get-Command scans third-party trees with a cold analysis cache.
// Keep the remaining paths so explicit custom module imports still work.
const builtinModulePrelude = "$env:PSModulePath = $PSHOME + '\\Modules;' + $env:PSModulePath; "

const utf8Prelude = "" +
	"$__caelisUtf8Encoding = [System.Text.Encoding]::UTF8; " +
	"$OutputEncoding = $__caelisUtf8Encoding; " +
	"$ProgressPreference = 'SilentlyContinue'; " +
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
