package appserver

import "strings"

// commandOperationIntent is shared by dispatch and receipt-only recovery.
func commandOperationIntent(principal Principal, action Action, base WriteBase, target string, request any) (OperationIntent, error) {
	digestRequest := request
	if principal.ApplicationID != "" || principal.ConnectionID != "" {
		digestRequest = struct {
			ApplicationID string
			ConnectionID  string
			Request       any
		}{principal.ApplicationID, principal.ConnectionID, request}
	}
	digest, err := requestDigest(digestRequest)
	if err != nil {
		return OperationIntent{}, err
	}
	ledgerPrincipal := strings.TrimSpace(principal.ID)
	if principal.ApplicationID != "" || principal.ConnectionID != "" {
		// Applications have independent operation namespaces under one Host
		// principal. The backend still receives the original authenticated owner.
		ledgerPrincipal, err = requestDigest([]string{principal.ID, principal.ApplicationID, principal.ConnectionID})
		if err != nil {
			return OperationIntent{}, err
		}
		ledgerPrincipal = "application-scope:" + ledgerPrincipal
	}
	return OperationIntent{
		PrincipalID: ledgerPrincipal, OperationID: strings.TrimSpace(base.OperationID), Action: action,
		SessionID: strings.TrimSpace(base.SessionID), Target: strings.TrimSpace(target), Digest: digest,
	}, nil
}
