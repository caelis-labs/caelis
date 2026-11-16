//go:build !windows

package gojaexec

import "os/exec"

func configureWorkerProcess(*exec.Cmd) {}
