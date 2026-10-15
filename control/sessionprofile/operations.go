package sessionprofile

// SessionID deterministically addresses a native Session for an admitted profile
// creation. Derivation itself grants neither ownership nor dispatch permission.
func SessionID(scope Scope, operationID string) string {
	return stableID("profile-", scope.PrincipalID, operationID)
}
