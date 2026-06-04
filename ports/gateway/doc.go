// Package gateway defines the public Caelis gateway contract.
//
// The gateway contract is the stable boundary between session surfaces,
// adapter code, and the local kernel implementation. Concrete orchestration
// lives in internal/kernel and should implement these interfaces rather than
// being re-exported.
package gateway
