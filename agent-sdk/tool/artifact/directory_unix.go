//go:build unix

package artifact

import (
	"os"
	"syscall"
)

func ownedArtifactDir(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func trustedTempParent(info os.FileInfo, tempRoot bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
		return false
	}
	return info.Mode().Perm()&0o022 == 0 || (tempRoot && info.Mode()&os.ModeSticky != 0)
}
