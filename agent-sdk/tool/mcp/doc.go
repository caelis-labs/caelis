// Package mcp implements MCP tool client, manager, and invocation runtime for
// the Agent SDK. Manager initializes servers concurrently and publishes immutable
// snapshots only after successful connection and tool listing. Individual startup
// failures are reported to the embedding owner without aborting other servers.
package mcp
