// policy.go ports codex's per-app consent gate (from the dissected
// SkyComputerUseService / codex-rs computer_use.rs): every computer-use
// action targets an app, and the app must be approved before loopy touches
// it. Approval is per bundle-id/app-name, session- or persistent-scoped,
// matching codex's `allow_persistent_approval` model.

package computer

import (
	"fmt"
	"strings"
	"sync"
)

// Policy gates app access. The zero value (nil map) denies everything —
// computer-use is off until configured or the user approves in-session.
type Policy struct {
	mu sync.Mutex
	// session holds in-memory approvals (bundle id or app name, lowered).
	session map[string]bool
	// allow/deny come from config (computer.allow / computer.deny) and
	// persist across sessions.
	allow map[string]bool
	deny  map[string]bool
	// DefaultDeny (true) means unlisted apps are denied; false = allowed.
	// Codex's default_app_access; we default to deny for safety.
	DefaultDeny bool
}

// NewPolicy builds a Policy from config lists (e.g. ["Google Chrome",
// "com.google.Chrome", "Safari"]).
func NewPolicy(allow, deny []string, defaultDeny bool) *Policy {
	p := &Policy{session: map[string]bool{}, allow: map[string]bool{}, deny: map[string]bool{}, DefaultDeny: defaultDeny}
	for _, a := range allow {
		p.allow[normalize(a)] = true
	}
	for _, d := range deny {
		p.deny[normalize(d)] = true
	}
	return p
}

func normalize(app string) string { return strings.ToLower(strings.TrimSpace(app)) }

// Check reports whether app may be driven, and why not when blocked.
// Deny list wins over allow list (codex: forbidden > policy-denied).
func (p *Policy) Check(app string) error {
	n := normalize(app)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deny[n] {
		return fmt.Errorf("computer-use is blocked from using %q by policy (computer.deny)", app)
	}
	if p.allow[n] || p.session[n] {
		return nil
	}
	if p.DefaultDeny {
		return &ApprovalNeeded{App: app}
	}
	return nil
}

// Approve records a session-scoped approval (from the TUI consent prompt).
func (p *Policy) Approve(app string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.session[normalize(app)] = true
}

// ApprovalNeeded signals that the app needs user consent this session.
// The tool layer surfaces a consent prompt; on yes, Policy.Approve(app)
// and retry.
type ApprovalNeeded struct{ App string }

func (e *ApprovalNeeded) Error() string {
	return fmt.Sprintf("computer-use needs approval to drive %q — approve in the prompt, or add it to computer.allow in ~/.loopy/config.json", e.App)
}
