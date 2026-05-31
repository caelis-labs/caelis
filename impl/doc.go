// Package impl is the root for replaceable Caelis implementations.
//
// Concrete packages below this root implement older ports and are wired only
// through composition roots such as internal/app/local. Production packages
// outside a composition root should not import implementation packages directly.
package impl
