// Package memorybinding retains only the opaque reference type used by ignored
// gatewayapp.Config inputs. It owns no Memory authority or persistence. Remove
// this compatibility type when those deprecated embedding inputs are removed.
package memorybinding

// BindingRef is a retired Memory selector accepted only for source compatibility.
// It does not identify an active runtime capability.
type BindingRef string
