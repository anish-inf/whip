#!/bin/sh
# Deprecated: whip now ships prebuilt binaries via GitHub Releases.
# The installer moved to the repo root:
#
#   curl -fsSL https://raw.githubusercontent.com/context-labs/whip/main/install.sh | sh
#
# This shim forwards to it (keeping the old URL working).
set -eu
exec sh -c "$(curl -fsSL https://raw.githubusercontent.com/context-labs/whip/main/install.sh)"
