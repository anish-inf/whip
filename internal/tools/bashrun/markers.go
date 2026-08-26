package bashrun

import (
	"os"
	"strconv"
)

// ChildMarkers are the env vars every bash child gets so scripts can detect
// they run under the agent (opencode sets AGENT=1, OPENCODE_PID). Session and
// model are stamped per-run by the caller via SetMarkers.
var ChildMarkers = []string{"WHIP=1"}

// SetMarkers records the session/model markers appended to every child env.
// Called once the session exists; idempotent.
func SetMarkers(sessionID, model string) {
	ChildMarkers = []string{"WHIP=1", "WHIP_SESSION_ID=" + sessionID, "WHIP_MODEL=" + model, "WHIP_PID=" + strconv.Itoa(os.Getpid())}
}
