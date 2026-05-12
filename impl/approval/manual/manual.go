// Package manual adapts surface-provided human approval callbacks to the
// approval port.
package manual

import (
	"context"
	"fmt"

	"github.com/OnslaughtSnail/caelis/ports/approval"
)

type Resolver func(context.Context, approval.Request) (approval.Decision, error)

type Approver struct {
	Resolve Resolver
}

func (a Approver) Decide(ctx context.Context, req approval.Request) (approval.Decision, error) {
	if a.Resolve == nil {
		return approval.Decision{}, fmt.Errorf("manual approval resolver is unavailable")
	}
	return a.Resolve(ctx, req)
}

var _ approval.Approver = Approver{}
