// Package gatewayapp is the current Caelis product Host composition root. It
// owns process-scoped authorities, concrete component assembly, Session Runtime
// activation and release, and ordered shutdown. It exposes Control capabilities
// through control/appserver services; presentation Surfaces must not depend on
// this package.
//
// Stack currently contains both Host and activated Runtime implementation
// details. Those private lifecycle responsibilities must be separated behind a
// narrow Runtime factory before either becomes a new stable package contract.
package gatewayapp
