//go:build !darwin && !linux

package shellproc

import (
	"os"
	"os/exec"
)

func configureGroup(_ *exec.Cmd) {}

func stopGroup(_ *exec.Cmd) error { return os.ErrProcessDone }
