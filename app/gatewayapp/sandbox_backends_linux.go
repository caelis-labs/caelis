//go:build linux

package gatewayapp

import (
	_ "github.com/caelis-labs/caelis/agent-sdk/sandbox/bwrap"
	_ "github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	_ "github.com/caelis-labs/caelis/agent-sdk/sandbox/landlock"
)
