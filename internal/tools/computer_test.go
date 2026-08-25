package tools

import (
	"strings"
	"testing"

	"github.com/context-labs/loopy/internal/computer"
)

// computer_exec refuses cleanly when no policy is installed (never drives
// an app ungated), and refuses unknown helpers with guidance.
func TestComputerExecGates(t *testing.T) {
	oldP, oldA := ComputerPolicy, ComputerApprover
	defer func() { ComputerPolicy, ComputerApprover = oldP, oldA }()
	ComputerPolicy, ComputerApprover = nil, nil

	// On Linux the platform check fires first.
	out := Execute(t.Context(), []Tool{ComputerExec()}, "computer_exec", []byte(`{"code":"print(chrome_state())"}`))
	if !strings.HasPrefix(out, "Error") {
		t.Fatalf("want error, got %q", out[:80])
	}
}

// The policy gate blocks denied apps and surfaces ApprovalNeeded for
// unlisted ones; an approver granting consent unblocks.
func TestGateApp(t *testing.T) {
	oldP, oldA := ComputerPolicy, ComputerApprover
	defer func() { ComputerPolicy, ComputerApprover = oldP, oldA }()

	ComputerPolicy = computer.NewPolicy([]string{"Google Chrome"}, []string{"Finder"}, true)
	ComputerApprover = nil

	if err := gateApp("Google Chrome"); err != nil {
		t.Errorf("allowed app blocked: %v", err)
	}
	if err := gateApp("Finder"); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("denied app must fail: %v", err)
	}
	err := gateApp("Safari")
	if err == nil {
		t.Fatal("unlisted app must need approval")
	}
	ComputerApprover = func(app string) bool { return app == "Safari" }
	if err := gateApp("Safari"); err != nil {
		t.Errorf("approver-consent must unblock: %v", err)
	}
	// persisted for the session
	ComputerApprover = nil
	if err := gateApp("Safari"); err != nil {
		t.Errorf("approval must persist for the session: %v", err)
	}
}
