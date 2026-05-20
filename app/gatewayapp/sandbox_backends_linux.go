//go:build linux

package gatewayapp

import (
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/bwrap"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/landlock"
)
