// Package chat provides the baseline chat agent implementation for agent runtime.
// It projects Runtime-authored compact checkpoints into provider-compatible
// history without changing their internal provenance. Large-result artifact
// pointers are Runtime metadata and remain separate from tool-owned hints;
// tool-authored reserved namespaces are removed at canonical ingress.
package chat
