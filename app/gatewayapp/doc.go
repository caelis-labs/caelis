// Package gatewayapp is the current Caelis product Host composition root. It
// owns process-scoped authorities and concrete component assembly. Its private
// Registry is constructed from explicit authorities and a narrow assembler
// without retaining the Host Stack; the Registry owns Session Runtime
// activation, release, and collective shutdown. The package exposes focused
// Host services to its private AppServer composition and product capabilities
// through control/appserver; presentation Surfaces must not depend on it.
//
// Stack owns process-lifetime Host services and a process-default Runtime
// composition. Each activated Session instead uses a private
// sessionRuntimeInstance with its own pinned composition and disposable
// resources. These private types are lifecycle boundaries, not stable product
// contracts or a second capability API.
package gatewayapp
