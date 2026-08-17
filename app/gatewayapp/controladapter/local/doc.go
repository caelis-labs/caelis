// Package local assembles the in-process AppServer service set from one
// gatewayapp Host. NewAppServer is the sole concrete Stack composition root;
// leaf services receive focused Host services, immutable views, and bound
// Control contracts instead of retaining the Stack.
package local
