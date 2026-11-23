//go:build !android

package plugin

import _ "embed"

//go:embed builtin/generated/x86_64-unknown-linux-gnu.tar.gz
var builtinDesktopWorldArchive []byte

const builtinDesktopWorldTarget = "x86_64-unknown-linux-gnu"
