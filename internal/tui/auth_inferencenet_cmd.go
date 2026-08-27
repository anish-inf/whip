package tui

import (
	"context"
	"strings"
	"time"

	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/inferencenet"
)

// /auth inference-net [key] connects Inference.net. The bare form runs the
// browser device-authorization login (no key handling); a pasted key goes
// through the same masked validate-and-upsert path as /auth openrouter.
//
// The device login is async: a goroutine runs the flow and reports back via
// inferenceNetAuthMsg so the UI goroutine owns all appends/config writes.

// authInferenceNetCommand dispatches the inference-net branch of /auth.
func (m *model) authInferenceNetCommand(args []string) {
	if len(args) > 1 {
		m.authInferenceNetKey(config.TrimKey(strings.Join(args[1:], "")), false)
		return
	}
	// Bare: browser device login (the smooth path). The paste-a-key route is
	// still available via `/auth inference-net <key>`.
	m.authInferenceNetLogin()
}

// inferenceNetAuthMsg carries a finished device login back to the UI.
type inferenceNetAuthMsg struct {
	auth inferencenet.Auth
	err  error
}

// authInferenceNetLogin runs the device-authorization flow in the background,
// then provisions a machine key and registers the provider.
func (m *model) authInferenceNetLogin() {
	m.append(dimStyle.Render("starting Inference.net sign-in… (approve in your browser)"))
	if m.prog == nil {
		return // tests drive applyInferenceNetAuth directly
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		auth, err := inferencenet.CompleteLogin(ctx, func(verificationURL, userCode string) {
			m.prog.Send(noticeMsg("approve this terminal in your browser:\n  " + verificationURL + "\n  code: " + userCode))
			if openBrowserURL(verificationURL) {
				m.prog.Send(noticeMsg("browser opened; waiting for approval…"))
			}
		})
		if err == nil {
			m.prog.Send(noticeMsg("provisioning an API key for this machine…"))
			_, err = auth.EnsureMachineKey(ctx)
			if err == nil {
				err = inferencenet.SaveAuth(auth)
			}
		}
		m.prog.Send(inferenceNetAuthMsg{auth: auth, err: err})
	}()
}

// applyInferenceNetAuth commits a finished device login on the UI goroutine:
// register the provider (machine key resolves from the stored auth file) and
// hot-swap the live agent when the session already routes inference-net.
func (m *model) applyInferenceNetAuth(msg inferenceNetAuthMsg) {
	if msg.err != nil {
		m.append(errStyle.Render("Inference.net sign-in failed: " + msg.err.Error()))
		return
	}
	m.cfg.UpsertInferenceNet("", false) // machine key resolves from disk
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
		return
	}
	m.append(dimStyle.Render("✓ signed in as " + msg.auth.UserEmail + " — project " + msg.auth.ProjectName + "; inference-net provider configured"))
	if m.prog != nil {
		go m.fetchCatalogs(true)
	}
}

// authInferenceNetKey validates a pasted Inference.net key and upserts the
// provider (mirrors the openrouter BYOK path).
func (m *model) authInferenceNetKey(key string, envMode bool) {
	if key == "" {
		m.append(errStyle.Render("/auth inference-net <key> needs a key (get one at https://inference.net)"))
		return
	}
	m.append(dimStyle.Render("validating key against Inference.net…"))
	if m.prog == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := inferencenet.ValidateKey(ctx, key)
		cancel()
		m.prog.Send(inferenceNetKeyMsg{key: key, envMode: envMode, err: err})
	}()
}

type inferenceNetKeyMsg struct {
	key     string
	envMode bool
	err     error
}

func (m *model) applyInferenceNetKey(msg inferenceNetKeyMsg) {
	if msg.err != nil {
		m.append(errStyle.Render("Inference.net rejected the key: " + msg.err.Error()))
		return
	}
	m.cfg.UpsertInferenceNet(msg.key, msg.envMode)
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
		return
	}
	m.append(dimStyle.Render("✓ inference-net configured; /model lists its models"))
	if m.prog != nil {
		go m.fetchCatalogs(true)
	}
}
