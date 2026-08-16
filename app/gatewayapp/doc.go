// Package gatewayapp is the current Caelis product Host composition root. It
// owns process-scoped authorities and concrete component assembly. Its private
// Registry is constructed from explicit authorities and a narrow assembler
// without retaining the Host Stack; the Registry owns Session Runtime
// activation, release, and collective shutdown. The package exposes Control
// capabilities through control/appserver services; presentation Surfaces must
// not depend on it.
//
// Activated Session Runtime composition still uses Stack as its implementation
// type, so Host and Runtime are not yet distinct stable package contracts.
package gatewayapp
