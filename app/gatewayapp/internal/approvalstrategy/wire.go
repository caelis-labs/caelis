package approvalstrategy

import (
	"github.com/OnslaughtSnail/caelis/impl/approval/agentreview"
	"github.com/OnslaughtSnail/caelis/impl/approval/deny"
	"github.com/OnslaughtSnail/caelis/impl/approval/manual"
	"github.com/OnslaughtSnail/caelis/ports/approval"
)

func AgentReviewApprover(reviewer approval.Reviewer) approval.Approver {
	return agentreview.Approver{Reviewer: reviewer}
}

func ManualApprover(resolve manual.Resolver) approval.Approver {
	return manual.Approver{Resolve: resolve}
}

func DenyApprover() approval.Approver {
	return deny.Approver{}
}
