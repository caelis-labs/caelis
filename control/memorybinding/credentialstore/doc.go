// Package credentialstore persists Memory issuer credentials behind opaque
// Control references. The credential bytes are Host-only and never enter
// AppConfig, Runtime tool arguments, Session history, or diagnostics.
package credentialstore
