// Package placement defines the transport-neutral durable execution choice
// selected by a host before work enters the Agent Runtime.
//
// Product profiles, credentials, endpoint lifecycle, and selection policy stay
// in the host Control layer. A Placement is a frozen value: Runtime consumers
// may verify and execute it, but must not resolve the opaque ProfileID again.
package placement
