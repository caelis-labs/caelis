// Package controladapter owns the Host-private, server-side dependency sets
// used to assemble Control services. Its exported constructors and projection
// types exist for repository-internal assembly and are not a downstream
// compatibility API: stable capabilities belong to control/appserver and
// focused control packages.
//
// The local subpackage is the only production package that consumes these
// sets directly. Its NewAppServer function is the sole concrete Stack
// composition root; it selects focused Host services, while Session-bound
// paths select focused services from an authorized Runtime lease. Leaf
// services receive only the dependency set they consume and bind
// principal-sensitive capabilities before use. The root package does not
// depend on the concrete Host. Other packages compose through local AppServer
// services or depend on focused Control contracts.
package controladapter
