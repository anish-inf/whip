package main

import (
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/inferencenet"
)

func TestAuthInferenceNetDispatch(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	// Unknown subcommand is rejected.
	if err := authCLI([]string{"inference-net", "bogus"}); err == nil {
		t.Error("unknown subcommand should error")
	}
	// key without "rotate" is rejected.
	if err := authCLI([]string{"inference-net", "key"}); err == nil {
		t.Error("`key` without rotate should error")
	}
	// The legacy "inference" provider name still routes.
	if err := authCLI([]string{"inference", "bogus"}); err == nil {
		t.Error("legacy alias should route to inference-net handler")
	}
}

func TestAuthInferenceNetBYOKNoKey(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	t.Setenv(config.InferenceNetEnvVar, "")
	if err := authCLI([]string{"inference-net", "login", "--key", ""}); err == nil {
		t.Error("BYOK with no key should error")
	}
}

func TestAuthInferenceNetStatusAndLogoutUnsigned(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	if err := authCLI([]string{"inference-net", "status"}); err != nil {
		t.Errorf("status on a fresh home should not error: %v", err)
	}
	// Logout with nothing stored is a clean no-op.
	if err := authCLI([]string{"inference-net", "logout"}); err != nil {
		t.Errorf("logout with no session should not error: %v", err)
	}
	// Rotate without a session tells the user to log in first.
	if err := authCLI([]string{"inference-net", "key", "rotate"}); err == nil ||
		!strings.Contains(err.Error(), "login") {
		t.Errorf("rotate without session should point at login, got %v", err)
	}
}

func TestAuthInferenceNetLogoutClearsStoredAuth(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	// Point the remote calls at a dead local port so they fail fast (and the
	// test never reaches the real relay); the local state is still cleared.
	defer inferencenet.SetURLsForTest("http://127.0.0.1:1", "", "")()
	if err := inferencenet.SaveAuth(inferencenet.Auth{SessionToken: "tok", MachineKey: "mk"}); err != nil {
		t.Fatal(err)
	}
	// Logout's remote calls fail soft (warnings); the local state is cleared.
	if err := authCLI([]string{"inference-net", "logout"}); err != nil {
		t.Errorf("logout should clear local state even when remote calls fail: %v", err)
	}
	a, _ := inferencenet.LoadAuth()
	if a != (inferencenet.Auth{}) {
		t.Errorf("logout should clear stored auth, got %+v", a)
	}
}
