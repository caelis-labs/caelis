package subagent

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

func TestPrivateDiagnosticErrorChainRetainsHiddenCauseAndBoundsPeerData(t *testing.T) {
	cause := errors.New("controlclient: observation subscription failed")
	err := errorcode.Wrap(errorcode.UnknownOutcome, "public summary", cause)
	chain := diagnosticErrorChain(err)
	if len(chain) != 2 || chain[0]["message"] != "public summary" || chain[1]["message"] != cause.Error() {
		t.Fatalf("hidden error chain = %#v", chain)
	}
	wide := make([]error, 32)
	for i := range wide {
		wide[i] = errors.New(strings.Repeat("界", 5000))
	}
	chain = diagnosticErrorChain(errors.Join(wide...))
	if len(chain) != 8 {
		t.Fatalf("chain length = %d", len(chain))
	}
	for _, node := range chain {
		if len(node["message"]) > 4096 || !utf8.ValidString(node["message"]) {
			t.Fatal("unbounded or invalid diagnostic message")
		}
	}
}
