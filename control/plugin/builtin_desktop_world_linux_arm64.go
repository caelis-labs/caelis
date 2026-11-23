//go:build !android

package plugin

import _ "embed"

//go:embed builtin/generated/aarch64-unknown-linux-gnu.tar.gz
var builtinDesktopWorldArchive []byte

const builtinDesktopWorldTarget = "aarch64-unknown-linux-gnu"
