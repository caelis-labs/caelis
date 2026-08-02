package gatewayapp

const guardianSceneID = "guardian"

func (s *Stack) newGuardianApprover() *guardianApprovalReviewer {
	return newGuardianApprovalApprover(s.Sessions)
}
