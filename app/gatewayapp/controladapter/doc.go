// Package controladapter owns the Host-private, server-side dependency views
// used to assemble Control services. It is not a product client API: stable
// capabilities belong to control/appserver and focused control packages.
//
// The local subpackage is the only production package that consumes these
// views directly. It owns translation from gatewayapp Host state and binds
// principal-sensitive capabilities before assembly; the root package does not
// depend on the concrete Host. Other packages compose through local AppServer
// services or depend on focused Control contracts.
package controladapter
