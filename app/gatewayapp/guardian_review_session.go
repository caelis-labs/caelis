package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/session"
)

func (r *guardianApprovalReviewer) reviewSessionFor(ctx context.Context, req gateway.ApprovalReviewRequest, activeSession session.Session) (*systemManagedAgentSession, error) {
	if r == nil || r.systemSessions == nil {
		return nil, fmt.Errorf("approval reviewer requires session history")
	}
	spec, ok := systemManagedAgentSpecFor(guardianProfileID)
	if !ok {
		return nil, fmt.Errorf("gatewayapp: missing %q system-managed agent", guardianProfileID)
	}
	reuseKey := guardianReuseKey(req.Model, guardianPolicyPrompt())
	return r.systemSessions.sessionFor(ctx, systemManagedAgentSessionRequest{
		ParentKey:     req.SessionRef.SessionID,
		ParentSession: activeSession,
		Spec:          spec,
		Purpose:       spec.Purpose,
		ReuseKey:      reuseKey,
	})
}

func guardianReviewSessionID(activeSession session.Session, reuseKey string) string {
	parentID := strings.TrimSpace(activeSession.SessionID)
	if parentID == "" {
		parentID = "approval-review"
	}
	reuseKey = strings.TrimSpace(reuseKey)
	if reuseKey == "" {
		return parentID + "-approval-review"
	}
	return parentID + "-approval-review-" + reuseKey
}

func guardianReuseKey(model model.LLM, policy string) string {
	hash := sha256.New()
	if model != nil {
		hash.Write([]byte(model.Name()))
	}
	hash.Write([]byte{0})
	hash.Write([]byte(policy))
	return hex.EncodeToString(hash.Sum(nil))
}
