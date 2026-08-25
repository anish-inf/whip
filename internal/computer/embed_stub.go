//go:build !darwin

// embed_stub.go — non-macOS platforms embed nothing; the native helper tier
// is unavailable (plan §Platforms: Linux/Windows are later ports behind the
// same JSON-RPC contract).

package computer

// ensureHelperBinary always fails off-darwin: there is no helper to run.
func ensureHelperBinary() (string, error) {
	return "", ErrUnsupportedPlatform
}
