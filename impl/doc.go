// Package impl is the root for replaceable Caelis implementations.
//
// Concrete packages below this root implement ports and are wired by
// app/gatewayapp. Production packages outside the composition root should not
// import implementation packages directly.
package impl
