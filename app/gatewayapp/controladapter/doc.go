// Package controladapter owns the Host-private, server-side dependency views
// used to assemble Control services. It is not a product client API: stable
// capabilities belong to control/appserver and focused control packages.
//
// The local subpackage is the only production package that consumes these
// views directly. Its NewAppServer function is the sole concrete Stack
// composition root; leaf services receive focused dependencies and bind
// principal-sensitive capabilities before use. The root package does not
// depend on the concrete Host. Other packages compose through local AppServer
// services or depend on focused Control contracts.
package controladapter
