package gatewayapp

const guardianSceneID = "guardian"

func (s *runtimeComposition) newGuardianApprover() *guardianApprovalReviewer {
	return newGuardianApprovalApprover(s.sessions)
}
