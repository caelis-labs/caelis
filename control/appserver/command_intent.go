package appserver

import "strings"

// commandOperationIntent is the one principal-owned command namespace.
func commandOperationIntent(principal Principal, action Action, base WriteBase, target string, request any) (OperationIntent, error) {
	digest, err := requestDigest(request)
	if err != nil {
		return OperationIntent{}, err
	}
	return OperationIntent{PrincipalID: strings.TrimSpace(principal.ID), OperationID: strings.TrimSpace(base.OperationID), Action: action, SessionID: strings.TrimSpace(base.SessionID), Target: strings.TrimSpace(target), Digest: digest}, nil
}
