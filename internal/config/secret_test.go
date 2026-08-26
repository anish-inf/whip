package config

import (
	"strings"
	"testing"
)

func TestResolveSecretEnvVar(t *testing.T) {
	t.Setenv("WHIP_SECRET_TEST", "s3cr3t")
	for _, ref := range []string{"$WHIP_SECRET_TEST", "${WHIP_SECRET_TEST}"} {
		got, err := ResolveSecret(ref)
		if err != nil || got != "s3cr3t" {
			t.Fatalf("%s: got %q err %v", ref, got, err)
		}
	}
}

func TestResolveSecretUnsetVar(t *testing.T) {
	for _, ref := range []string{"$WHIP_SECRET_UNSET", "${WHIP_SECRET_UNSET}"} {
		got, err := ResolveSecret(ref)
		if err == nil || !strings.Contains(err.Error(), "WHIP_SECRET_UNSET") {
			t.Fatalf("%s: expected unset-var error naming the var, got %q err %v", ref, got, err)
		}
		if got != "" {
			t.Fatalf("%s: expected empty on error, got %q", ref, got)
		}
	}
}

func TestResolveSecretCommand(t *testing.T) {
	got, err := ResolveSecret("!printf secret-value")
	if err != nil || got != "secret-value" {
		t.Fatalf("!printf: got %q err %v", got, err)
	}
	// Failing command errors; the reference, not stderr secrets, is in the message.
	if _, err := ResolveSecret("!exit 1"); err == nil {
		t.Fatal("!exit 1: expected error")
	}
}

func TestResolveSecretLiteral(t *testing.T) {
	got, err := ResolveSecret("sk-literal-key-123")
	if err != nil || got != "sk-literal-key-123" {
		t.Fatalf("literal: got %q err %v", got, err)
	}
	// A trailing/embedded $ is not a reference.
	got, err = ResolveSecret("price is $5")
	if err != nil || got != "price is $5" {
		t.Fatalf("embedded $: got %q err %v", got, err)
	}
}

func TestProviderHoldsReferenceNotValue(t *testing.T) {
	t.Setenv("WHIP_SECRET_TEST", "resolved-value")

	p := Provider{Name: "test", BaseURL: "https://other.example.com", APIKey: "${WHIP_SECRET_TEST}"}
	if p.APIKey != "${WHIP_SECRET_TEST}" {
		t.Fatalf("config must hold the raw reference, got %q", p.APIKey)
	}
	k, err := p.ResolveKey()
	if err != nil || k != "resolved-value" {
		t.Fatalf("ResolveKey: got %q err %v", k, err)
	}
	// Key() degrades unresolvable references to "" (missing-key path).
	p.APIKey = "$WHIP_SECRET_UNSET"
	if k := p.Key(); k != "" {
		t.Fatalf("unset ref should yield empty key, got %q", k)
	}
	if _, err := p.ResolveKey(); err == nil || !strings.Contains(err.Error(), "WHIP_SECRET_UNSET") {
		t.Fatalf("ResolveKey should name the unset var: %v", err)
	}
	// apiKeyEnv still wins over an apiKey reference.
	p.APIKeyEnv = "WHIP_SECRET_TEST"
	if k, _ := p.ResolveKey(); k != "resolved-value" {
		t.Fatalf("apiKeyEnv precedence: got %q", k)
	}
}
