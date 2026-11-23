//go:build !(darwin && arm64) && !(linux && !android && (amd64 || arm64)) && !(windows && amd64)

package plugin

var builtinDesktopWorldArchive []byte

const builtinDesktopWorldTarget = ""
