package winps

import "strings"

type Options struct {
	TTY         bool
	Interactive bool
}

func Args(command string, opts Options) []string {
	args := []string{"-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass"}
	if !opts.TTY && !opts.Interactive {
		args = append(args, "-NonInteractive")
	}
	return append(args, "-Command", Command(command))
}

func Command(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return utf8Prelude
	}
	return utf8Prelude + command
}

const utf8Prelude = "$__caelisUtf8Encoding = New-Object System.Text.UTF8Encoding $false; [Console]::InputEncoding = $__caelisUtf8Encoding; [Console]::OutputEncoding = $__caelisUtf8Encoding; $OutputEncoding = $__caelisUtf8Encoding; "
