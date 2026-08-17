// Package gatewayapp is the current Caelis product Host composition root. It
// owns process-scoped authorities and concrete component assembly. Its private
// Registry is constructed from explicit authorities and a narrow assembler
// without retaining the Host Stack or root Runtime composition. Mutable process
// configuration has one mutable Host owner and is sampled through an
// independent source at activation. The root active Runtime is an installed
// execution artifact rather than a parallel publication target. The Registry
// owns Session Runtime activation, release, and collective shutdown.
// The package exposes focused Host services to its private AppServer
// composition and product capabilities through control/appserver. Authorized
// Session Runtime leases expose only focused inputs selected by that private
// composition; reconnect observation and Task child-history fallback use
// dedicated readers rather than Stack pass-throughs, as does Participant
// handle projection. Execution and Plugin writes remain on principal-bound
// AppServer commands, while configuration revision reads use a focused status
// service rather than direct Stack mirrors. Presentation Surfaces must not
// depend on this package.
//
// Stack owns process-lifetime Host services and a process-default Runtime
// composition. Each activated Session instead uses a private
// sessionRuntimeInstance with its own pinned composition and disposable
// resources. These private types are lifecycle boundaries, not stable product
// contracts or a second capability API.
package gatewayapp
