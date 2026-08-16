// Package controladapter owns the Host-private, server-side dependency views
// used to assemble Control services. It is not a product client API: stable
// capabilities belong to control/appserver and focused control packages.
//
// The local subpackage is the only production package that consumes these
// views directly. Other packages compose through its AppServer services or
// depend on focused Control contracts.
package controladapter
