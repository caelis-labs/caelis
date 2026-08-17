// Package local assembles the in-process AppServer service set from one
// gatewayapp Host. NewAppServer is the sole concrete Stack composition root;
// leaf services receive focused Host or authorized Runtime services and bound
// Control contracts instead of retaining the Stack or a wide Runtime view.
package local
