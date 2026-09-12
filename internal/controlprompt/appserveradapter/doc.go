// Package appserveradapter adapts the aggregate AppServer client boundary to
// the private prompt and presentation contracts shared by Caelis Surfaces.
// It owns no Host, Runtime, persistence, or transport authority.
// Selected Session observations remain attached across Turns until explicitly
// closed. Closing a view releases its feed, while Interrupt addresses the exact
// observed Turn through Control. Session execution leases remain Host-owned.
package appserveradapter
