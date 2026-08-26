package bashrun

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Bash children carry the agent markers so scripts can detect they're under whip.
func TestChildMarkers(t *testing.T) {
	SetMarkers("sess123", "kimi-k3-fast")
	res := Run(context.Background(), Options{Command: "env", Timeout: 5 * time.Second})
	for _, want := range []string{"WHIP=1", "WHIP_SESSION_ID=sess123", "WHIP_MODEL=kimi-k3-fast", "WHIP_PID="} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("child env missing %q:\n%s", want, res.Output)
		}
	}
}
