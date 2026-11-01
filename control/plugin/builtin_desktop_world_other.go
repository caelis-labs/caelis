//go:build !(darwin && arm64) && !(windows && amd64)

package plugin

var builtinDesktopWorldArchive []byte

const builtinDesktopWorldTarget = ""
