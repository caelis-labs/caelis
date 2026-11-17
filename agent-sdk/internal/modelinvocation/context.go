// Package modelinvocation shares private actual-attempt hook context identity
// between the SDK model owner and the built-in chat loop.
package modelinvocation

// MiddlewareKey carries the SDK-owned continuation. Extension contexts omit it.
type MiddlewareKey struct{}
