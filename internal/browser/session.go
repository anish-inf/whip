// session.go manages Browser instances per named session for the agent
// tools: one Backend per (mode, name), lazily opened, serialized per
// session, self-healing on dead connections. Named sessions give parallel
// subagents isolated tabs/browsers (§5b) without cross-process daemons.

package browser

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// sessionNameRe is browser-harness's BU_NAME guard: filesystem- and
// socket-safe names only.
var sessionNameRe = regexp.MustCompile(`\A[A-Za-z0-9_-]{1,64}\z`)

// Manager hands out per-session backends. Not safe for concurrent use
// outside of Session, which serializes each session's calls.
type Manager struct {
	mu       sync.Mutex
	mode     Mode
	sessions map[string]*Session
}

// NewManager creates a Manager for the given default mode.
func NewManager(mode Mode) *Manager {
	return &Manager{mode: mode, sessions: map[string]*Session{}}
}

// Session is one named browser session: a lazily-opened Backend whose
// calls are serialized through a 1-capacity channel semaphore (the
// filelocks idiom — two calls to one browser must not interleave, while
// different sessions run truly in parallel).
type Session struct {
	name    string
	mode    Mode
	sem     chan struct{}
	mu      sync.Mutex
	backend Backend
	noticed bool // fallback notice already emitted for this backend
}

// fallbackNotice is the one-line heads-up prepended to the first tool
// output when a live-mode session fell back to a launched loopy Chrome
// (hermes /browser connect's "launched and listening" line, in-band so the
// model relays it in context). Emitted once per session.
const fallbackNotice = "[Note: no debuggable live browser found — using loopy's dedicated Chrome (logins live in its own profile). To drive your everyday browser instead: chrome://inspect/#remote-debugging, or set browser.mode/cdpUrl in config.]\n\n"

// Session returns the named session (default name "default"), validating
// the name. The mode is the manager's default; per-session mode override
// comes from "<mode>:<name>" prefixes in the tool (e.g. "headless:scrape").
func (m *Manager) Session(name string) (*Session, error) {
	mode := m.mode
	if i := strings.Index(name, ":"); i > 0 {
		prefix := Mode(name[:i])
		switch prefix {
		case ModeLive, ModeDedicated, ModeHeadless, ModeExtension:
			mode, name = prefix, name[i+1:]
		default:
			return nil, fmt.Errorf("invalid session %q: unknown mode prefix %q (live|dedicated|headless|extension)", name, prefix)
		}
	}
	if name == "" {
		name = "default"
	}
	if !sessionNameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid session name %q: use 1-64 letters, digits, dashes, or underscores", name)
	}
	key := string(mode) + ":" + name
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	if !ok {
		s = &Session{name: name, mode: mode, sem: make(chan struct{}, 1)}
		m.sessions[key] = s
	}
	return s, nil
}

// Do runs fn with the session's live backend, holding the session lock.
// A dead backend is reopened once (stale-tab/browser-closed recovery);
// reopen errors are returned for the caller to surface.
func (s *Session) Do(ctx context.Context, fn func(b Backend) (string, error)) (string, error) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	b, err := s.get(ctx)
	if err != nil {
		return "", err
	}
	out, err := fn(b)
	if err != nil && isConnErr(err) {
		s.drop()
		b, rerr := s.get(ctx)
		if rerr != nil {
			return "", err // original error is more useful than the reopen's
		}
		out, err = fn(b)
	}
	// Only consume the once-per-session notice on success — an erroring call
	// may drop out, and the notice must not be consumed unseen.
	if err == nil && out != "" {
		out = s.takeNotice(b) + out
	}
	return out, err
}

// takeNotice returns the fallback notice exactly once per session, and only
// when a live-mode session is actually driving a launched/reattached loopy
// Chrome (the user asked for live; they should hear they didn't get it).
func (s *Session) takeNotice(b Backend) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noticed || s.mode != ModeLive || b.Obtained() == ObtainedLive {
		return ""
	}
	s.noticed = true
	return fallbackNotice
}

func (s *Session) get(ctx context.Context) (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend != nil {
		return s.backend, nil
	}
	b, err := openNamed(ctx, s.mode, s.name)
	if err != nil {
		return nil, err
	}
	s.backend = b
	s.noticed = false // a fresh backend earns a fresh notice
	return b, nil
}

// openNamed is the package-level OpenNamed, indirected so session tests
// substitute fakes without launching a browser.
var openNamed = func(ctx context.Context, mode Mode, name string) (Backend, error) {
	return OpenNamed(ctx, mode, name)
}

func (s *Session) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend != nil {
		s.backend.Close()
		s.backend = nil
	}
}

// SwitchDriver sets the active driver and closes every open session so the
// next browser_exec reopens on the new driver. Live-mode sessions detach
// (the user's Chrome is untouched); dedicated/headless sessions are killed.
func (m *Manager) SwitchDriver(d string) {
	SetDriver(d)
	m.CloseAll()
}

// CloseAll closes every session's backend. Live/dedicated sessions detach
// (the Chrome survives as a reattach target); headless sessions kill their
// launched Chrome.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		s.drop()
	}
}

// isConnErr reports whether err looks like a dead CDP connection
// (browser closed, tab crashed) rather than a page-level failure.
func isConnErr(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := e.Error()
		if strings.Contains(msg, "websocket") && (strings.Contains(msg, "closed") || strings.Contains(msg, "close 100")) {
			return true
		}
		if strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") || strings.Contains(msg, "EOF") {
			return true
		}
	}
	return false
}
