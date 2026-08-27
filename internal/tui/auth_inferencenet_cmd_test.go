package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/inferencenet"
)

func TestAuthInferenceNetDispatch(t *testing.T) {
	m := authTestModel(t)
	// The legacy "inference" alias routes to the same handler.
	m.authCommand([]string{"inference"})
	if !strings.Contains(m.transcriptText(), "sign-in") {
		t.Errorf("bare /auth inference should start device login:\n%s", m.transcriptText())
	}
}

func TestApplyInferenceNetKeyUpsertsProvider(t *testing.T) {
	m := authTestModel(t)
	m.applyInferenceNetKey(inferenceNetKeyMsg{key: "inf-good"})
	p, ok := m.cfg.Providers[config.InferenceNetProvider]
	if !ok {
		t.Fatal("inference-net provider not upserted")
	}
	if p.APIKey != "inf-good" {
		t.Errorf("key not stored: %+v", p)
	}
	if !strings.Contains(m.transcriptText(), "inference-net configured") {
		t.Errorf("no confirmation appended:\n%s", m.transcriptText())
	}
}

func TestApplyInferenceNetAuthUsesMachineKey(t *testing.T) {
	m := authTestModel(t)
	auth := inferencenet.Auth{UserEmail: "abe@x.dev", ProjectName: "Primary"}
	m.applyInferenceNetAuth(inferenceNetAuthMsg{auth: auth})
	p := m.cfg.Providers[config.InferenceNetProvider]
	if p.APIKey != "" || p.APIKeyEnv != "" {
		t.Errorf("device login should leave key fields empty (machine key resolves from disk): %+v", p)
	}
	if !strings.Contains(m.transcriptText(), "signed in as abe@x.dev") {
		t.Errorf("no sign-in confirmation:\n%s", m.transcriptText())
	}
}

func TestApplyInferenceNetAuthError(t *testing.T) {
	m := authTestModel(t)
	m.applyInferenceNetAuth(inferenceNetAuthMsg{err: errors.New("denied")})
	if !strings.Contains(m.transcriptText(), "sign-in failed") {
		t.Errorf("error not surfaced:\n%s", m.transcriptText())
	}
}
