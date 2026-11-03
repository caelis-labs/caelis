//go:build !windows

package mcp

import "os/exec"

func configureStdioProcess(*exec.Cmd) {}
