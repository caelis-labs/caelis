//go:build !windows

package adapterhostclient

import (
	"errors"
	"os"
)

func secureChannelGrantFile(file *os.File) error {
	if file == nil {
		return errors.New("adapterhostclient: channel grant file is unavailable")
	}
	return file.Chmod(0o600)
}

func validateChannelGrantFileSecurity(_ *os.File, info os.FileInfo) error {
	if info == nil || info.Mode().Perm() != 0o600 {
		return errors.New("adapterhostclient: channel grant file must have mode 0600")
	}
	return nil
}
