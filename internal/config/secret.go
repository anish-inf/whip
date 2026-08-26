package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SecretCmdTimeout bounds a "!cmd" secret reference.
const SecretCmdTimeout = 5 * time.Second

// ResolveSecret resolves a secret-by-reference configured value at the point
// of use. The config file stores the REFERENCE, never the resolved value:
//
//	"$NAME" / "${NAME}" → os.Getenv(NAME); error if unset or empty.
//	"!cmd args..."      → trimmed stdout of the command; error on failure.
//	anything else       → returned as-is (a literal key; backwards compatible).
//
// This is the exo secrets-as-references pattern (secret_id indirection —
// docs/learnings/other-harnesses/exo.md §10): resolution happens when the
// secret is actually needed for a request, never at load or save, so config
// and the session store hold only references. Resolved values must never be
// passed to LogEvent or written to the event log.
func ResolveSecret(v string) (string, error) {
	switch {
	case strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}"):
		name := v[2 : len(v)-1]
		if val := os.Getenv(name); val != "" {
			return val, nil
		}
		return "", fmt.Errorf("secret reference ${%s}: environment variable unset or empty", name)
	case strings.HasPrefix(v, "$") && len(v) > 1 && !strings.ContainsAny(v[1:], " \t"):
		name := v[1:]
		if val := os.Getenv(name); val != "" {
			return val, nil
		}
		return "", fmt.Errorf("secret reference $%s: environment variable unset or empty", name)
	case strings.HasPrefix(v, "!"):
		fields := strings.Fields(v[1:])
		if len(fields) == 0 {
			return "", fmt.Errorf("secret reference %q: empty command", v)
		}
		ctx, cancel := context.WithTimeout(context.Background(), SecretCmdTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, fields[0], fields[1:]...).Output()
		if err != nil {
			return "", fmt.Errorf("secret reference %q: %w", v, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	return v, nil
}
