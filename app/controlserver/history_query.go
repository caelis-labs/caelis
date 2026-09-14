package controlserver

import (
	"net/http"
	"strconv"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/history"
)

func historyTurnsQuery(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("history_turns")
	if raw == "" {
		return 0, true
	}
	turns, err := strconv.Atoi(raw)
	if err != nil || turns < 0 || turns > history.MaxTurns {
		writeMappedError(w, errorcode.New(errorcode.InvalidArgument, "Invalid history window"))
		return 0, false
	}
	return turns, true
}
