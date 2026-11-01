package plugin

import _ "embed"

//go:embed builtin/generated/x86_64-pc-windows-msvc.zip
var builtinDesktopWorldArchive []byte

const builtinDesktopWorldTarget = "x86_64-pc-windows-msvc"
