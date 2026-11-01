package plugin

import _ "embed"

//go:embed builtin/generated/aarch64-apple-darwin.zip
var builtinDesktopWorldArchive []byte

const builtinDesktopWorldTarget = "aarch64-apple-darwin"
