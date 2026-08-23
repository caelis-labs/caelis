package session

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// NewSessionFenceClaim attaches a one-shot, unforgeable mutation claim to a
// newly acquired fence. Stores persist only its digest and must not expose the
// claim through SessionFenceReader.
func NewSessionFenceClaim(fence SessionFence) (SessionFence, error) {
	if !SessionFenceIsHeld(fence) || strings.TrimSpace(fence.OwnerID) == "" || fence.FencingToken == 0 {
		return SessionFence{}, &FenceConflictError{
			SessionID: NormalizeSessionRef(fence.SessionRef).SessionID,
			Detail:    "complete fence identity is required before creating a claim",
		}
	}
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return SessionFence{}, err
	}
	fence.claimToken = hex.EncodeToString(value[:])
	fence.claimDigest = sessionFenceClaimDigest(fence.claimToken)
	return fence, nil
}

// SessionFenceForStorage removes the bearer claim while retaining the digest
// required to authorize future mutations and exact release.
func SessionFenceForStorage(fence SessionFence) SessionFence {
	fence.claimToken = ""
	return fence
}

// SessionFenceForObservation removes all claim material from a fence returned
// by a read-only observation API.
func SessionFenceForObservation(fence SessionFence) SessionFence {
	fence.claimToken = ""
	fence.claimDigest = ""
	return fence
}

// SessionFenceClaimDigest returns the non-bearer digest stored with a fence.
func SessionFenceClaimDigest(fence SessionFence) string {
	return strings.TrimSpace(fence.claimDigest)
}

// RestoreSessionFenceClaimDigest restores a persisted non-bearer digest.
func RestoreSessionFenceClaimDigest(fence SessionFence, digest string) SessionFence {
	fence.claimToken = ""
	fence.claimDigest = strings.TrimSpace(digest)
	return fence
}

// SessionFenceHasClaim reports whether fence carries the exact acquisition
// claim matching its stored digest.
func SessionFenceHasClaim(fence SessionFence) bool {
	return sessionFenceClaimMatches(fence.claimDigest, fence.claimToken)
}

// SessionFenceReleaseRequest creates an exact-release request from the fence
// returned by acquisition. A fence returned by SessionFenceReader cannot
// produce an authorized request because observation removes its bearer claim.
func SessionFenceReleaseRequest(fence SessionFence) ReleaseSessionFenceRequest {
	return ReleaseSessionFenceRequest{
		SessionRef: fence.SessionRef,
		FenceID:    fence.FenceID,
		OwnerID:    fence.OwnerID,
		claimToken: fence.claimToken,
	}
}

// SessionFenceReleaseAuthorized reports whether req carries the exact bearer
// claim for active. Stores call it while holding their transaction lock.
func SessionFenceReleaseAuthorized(active SessionFence, req ReleaseSessionFenceRequest) bool {
	return NormalizeSessionRef(active.SessionRef) == NormalizeSessionRef(req.SessionRef) &&
		strings.TrimSpace(active.FenceID) != "" &&
		active.FenceID == strings.TrimSpace(req.FenceID) &&
		active.OwnerID == strings.TrimSpace(req.OwnerID) &&
		sessionFenceClaimMatches(active.claimDigest, req.claimToken)
}

func sessionFenceClaimMatches(digest, token string) bool {
	digest = strings.TrimSpace(digest)
	token = strings.TrimSpace(token)
	if digest == "" || token == "" {
		return false
	}
	actual := sessionFenceClaimDigest(token)
	return subtle.ConstantTimeCompare([]byte(digest), []byte(actual)) == 1
}

func sessionFenceClaimDigest(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
