//go:build linux

package gatewayapp

import (
	_ "github.com/caelis-labs/caelis/impl/sandbox/bwrap"
	_ "github.com/caelis-labs/caelis/impl/sandbox/host"
	_ "github.com/caelis-labs/caelis/impl/sandbox/landlock"
)
