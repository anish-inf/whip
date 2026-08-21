#!/bin/sh
# loopy installer — go install github.com/context-labs/loopy/cmd/loopy@latest
#
#   curl -fsSL https://raw.githubusercontent.com/context-labs/loopy/main/scripts/install.sh | sh
#
set -eu

MODULE="github.com/context-labs/loopy/cmd/loopy@latest"

if ! command -v go >/dev/null 2>&1; then
	echo "loopy: Go is required (>= 1.27). Install it from https://go.dev/dl/ and re-run." >&2
	exit 1
fi

echo "Installing loopy with go install $MODULE …"
# Private repo? Bypass the public proxy and checksum DB so git+ssh auth is used.
if ! go install "$MODULE" 2>/dev/null; then
	echo "Falling back to direct fetch (GOPRIVATE) for private repos…"
	GOPRIVATE="github.com/context-labs" GOFLAGS=-mod=mod GONOSUMDB="github.com/context-labs" \
		GONOSUMCHECK=1 GOPROXY=direct GOSUMDB=off go install "$MODULE"
fi

GOBIN="$(go env GOBIN)"
if [ -z "$GOBIN" ]; then
	GOBIN="$(go env GOPATH)/bin"
fi

echo "Installed to $GOBIN/loopy"
case ":$PATH:" in
*":$GOBIN:"*) ;;
*) echo "Note: $GOBIN is not on your PATH — add it, or run: $GOBIN/loopy" ;;
esac
