// Package tui defines the gateway adapter boundary for terminal UI surfaces.
//
// The top-level tui/ tree remains the presentation shell. Only the
// gateway-facing runtime bridge lives under gateway/adapter/tui so adapters can
// depend on the stable root gateway contract without importing gateway/core.
package tui
