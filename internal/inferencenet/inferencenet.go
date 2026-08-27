// Package inferencenet implements first-class Inference.net auth for whip:
// the browser-based device-authorization login (bring-your-own-account, no key
// handling) plus machine API-key provisioning over the relay's tRPC surface.
// It mirrors the @inference/fast CLI's flow (apps/fast-cli/src/lib/auth.ts).
package inferencenet

// DeviceClientID is registered in the relay's device-authorization allowlist
// (packages/auth/src/device-authorization.ts) — the approval page names it.
const DeviceClientID = "whip"

const (
	// BaseURL is the OpenAI-compatible inference gateway root.
	BaseURL = "https://api.inference.net/v1"
	// EnvVar is the environment variable the provider entry reads its key
	// from in env mode.
	EnvVar = "INFERENCE_API_KEY"
)

var (
	// relayURL is the control-plane (auth/session/org/api-key) API.
	relayURL = "https://observability-api.inference.net"
	// dashboardURL hosts the device-approval page the user authorizes from.
	dashboardURL = "https://inference.net"
	// baseURL mirrors BaseURL; a var so tests can point at a stub gateway.
	baseURL = BaseURL
)

// trpcEndpoint is where the relay mounts the rich appRouter (project, apiKey,
// …): the host root, superjson transport. ("/api/trpc" is a separate, narrow
// relay-compat router that does NOT have these procedures.)
const trpcEndpoint = ""

// teamIDHeader carries the active team on relay tRPC calls.
const teamIDHeader = "x-inference-team-id"
