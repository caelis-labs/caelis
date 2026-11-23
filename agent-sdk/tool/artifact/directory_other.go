//go:build !unix

package artifact

import "os"

// Access control comes from the host's temporary directory ACL on Windows.
func ownedArtifactDir(os.FileInfo) bool        { return true }
func trustedTempParent(os.FileInfo, bool) bool { return true }
