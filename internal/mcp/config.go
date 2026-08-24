// Package mcp implements loopy's Model Context Protocol support: a client that
// connects to configured MCP servers (stdio and streamable HTTP) and exposes
// their tools to the agent loop, plus a server (`loopy mcp serve`) exposing
// loopy's own tools.
//
// Configuration is backwards compatible with claude-style (.mcp.json project
// files) and codex-style (~/.codex/config.toml [mcp_servers]) formats; both
// are normalized into ServerConfig and merged with loopy's own "mcp" block in
// ~/.loopy/config.json, which always wins on name conflicts.
package mcp

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/context-labs/loopy/internal/config"
)

// ServerConfig is loopy's normalized MCP server definition. Claude-style
// (type: stdio/http/sse, command+args+env, url+headers) and codex-style
// (command/args/headers/startup_timeout_sec) entries both parse into this
// shape. A server is stdio when Command is set, remote when URL is set.
type ServerConfig struct {
	// stdio
	Command []string          `json:"command,omitempty"` // argv: program + arguments
	Env     map[string]string `json:"env,omitempty"`     // extra env (layered over loopy's own environment)
	Cwd     string            `json:"cwd,omitempty"`     // working directory; "" = loopy's cwd
	// remote
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// common
	Enabled        *bool  `json:"enabled,omitempty"`        // nil = enabled
	Note           string `json:"note,omitempty"`           // surfaced in /mcp status (e.g. unsupported import)
	StartupTimeout int    `json:"startupTimeout,omitempty"` // seconds to connect + list tools (default 30)
	ToolTimeout    int    `json:"toolTimeout,omitempty"`    // seconds per tool call (default 60)
}

// Remote reports whether the server connects over HTTP rather than stdio.
func (c ServerConfig) Remote() bool { return c.URL != "" }

// Disabled reports whether the server is explicitly turned off.
func (c ServerConfig) Disabled() bool { return c.Enabled != nil && !*c.Enabled }

// StartupTimeoutDuration bounds connect + tools/list for the server.
func (c ServerConfig) StartupTimeoutDuration() time.Duration {
	if c.StartupTimeout > 0 {
		return time.Duration(c.StartupTimeout) * time.Second
	}
	return 30 * time.Second
}

// ToolTimeoutDuration bounds one tool call (the model's ctx may cancel sooner).
func (c ServerConfig) ToolTimeoutDuration() time.Duration {
	if c.ToolTimeout > 0 {
		return time.Duration(c.ToolTimeout) * time.Second
	}
	return 60 * time.Second
}

// Valid reports a config error, if any; "" means usable.
func (c ServerConfig) Valid() string {
	switch {
	case c.Remote() && len(c.Command) > 0:
		return "both command and url set"
	case !c.Remote() && len(c.Command) == 0:
		return "neither command nor url set"
	case c.Remote() && c.URL != "" && !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://"):
		return "url must start with http:// or https://"
	}
	return ""
}

var notNameChar = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitize maps a name to the provider-safe charset, as in opencode's
// mcp/catalog.ts (`[^a-zA-Z0-9_-] → "_"`).
func sanitize(s string) string {
	if s == "" {
		return "_"
	}
	return notNameChar.ReplaceAllString(s, "_")
}

// serverKey derives the unique identifier embedded in tool names for a
// configured server. Names already in the safe charset pass through
// unchanged (keeping tool names short and greppable); names needing
// sanitization — including "__" runs, which would break the ParseToolName
// split — get a short hash of the ORIGINAL name appended, so "a.b" and "a b"
// (which both sanitize to "a_b" — a collision opencode ships) stay distinct.
func serverKey(name string) string {
	if name != "" && notNameChar.FindStringIndex(name) == nil && !strings.Contains(name, "__") {
		return name // already safe and "__"-free
	}
	sum := fnv.New32a()
	sum.Write([]byte(name))
	return fmt.Sprintf("%s_%08x", strings.ReplaceAll(sanitize(name), "_", "-"), sum.Sum32())
}

// ToolName derives the agent-facing tool name: "mcp__<serverKey>__<tool>" —
// the claude-code convention. Double underscores make the split unambiguous:
// server keys and sanitized tool names never contain "__".
func ToolName(server, tool string) string {
	return "mcp__" + serverKey(server) + "__" + sanitize(tool)
}

// ParseToolName splits an agent-facing MCP tool name back into the server's
// key and the (sanitized) tool name. The manager keys servers by serverKey,
// so the returned server key identifies the server uniquely. ok is false
// when name is not an MCP tool name.
func ParseToolName(name string) (srvKey, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, "mcp__")
	if !found {
		return "", "", false
	}
	srvKey, tool, found = strings.Cut(rest, "__")
	if !found || tool == "" {
		return "", "", false
	}
	return srvKey, tool, true
}

// Merge combines server configs by name: loopy's own config wins whole-entry
// over codex, which wins over a project's .mcp.json. No field-level merging —
// predictable, and matches how claude/codex treat their own scopes.
func Merge(loopy, codex, claude map[string]ServerConfig) map[string]ServerConfig {
	out := make(map[string]ServerConfig, len(loopy)+len(codex)+len(claude))
	for name, cfg := range claude {
		out[name] = cfg
	}
	for name, cfg := range codex {
		out[name] = cfg
	}
	for name, cfg := range loopy {
		out[name] = cfg
	}
	return out
}

// ImportPolicy selects which imported (non-loopy) server definitions loopy
// picks up. The zero value imports both sources with no name filtering —
// the pre-gating behavior. ImportPolicyFrom converts the config-file block.
type ImportPolicy struct {
	Claude ImportSourcePolicy
	Codex  ImportSourcePolicy
}

// ImportSourcePolicy gates one import source. Enabled=false drops the source
// entirely; Only (non-empty) is a name allowlist; Exclude is a denylist and
// wins over Only when both are set.
type ImportSourcePolicy struct {
	Enabled bool
	Only    map[string]bool
	Exclude map[string]bool
}

// ImportPolicyFrom converts the config-file mcpImport block into the merge
// policy. imp may be nil (import everything).
func ImportPolicyFrom(imp *config.MCPImport) ImportPolicy {
	convert := func(s *config.MCPImportSource) ImportSourcePolicy {
		p := ImportSourcePolicy{Enabled: true}
		if s == nil {
			return p
		}
		if s.Enabled != nil {
			p.Enabled = *s.Enabled
		}
		if len(s.Only) > 0 {
			p.Only = make(map[string]bool, len(s.Only))
			for _, n := range s.Only {
				p.Only[n] = true
			}
		}
		if len(s.Exclude) > 0 {
			p.Exclude = make(map[string]bool, len(s.Exclude))
			for _, n := range s.Exclude {
				p.Exclude[n] = true
			}
		}
		return p
	}
	if imp == nil {
		return ImportPolicy{Claude: convert(nil), Codex: convert(nil)}
	}
	return ImportPolicy{Claude: convert(imp.Claude), Codex: convert(imp.Codex)}
}

// Admits reports whether a server name passes the source's gate.
func (p ImportSourcePolicy) Admits(name string) bool {
	if !p.Enabled {
		return false
	}
	if p.Exclude[name] {
		return false // denylist wins over allowlist
	}
	if len(p.Only) > 0 && !p.Only[name] {
		return false
	}
	return true
}

// Filtered is the discovery result when an ImportPolicy is applied: Merged is
// what the manager connects to; Blocked holds the servers the policy filtered
// out, forced disabled with a note so they stay visible (/mcp, loopy mcp
// list) instead of vanishing silently. Blocked never shadows a loopy entry of
// the same name. Sources attributes every discovered name (merged or
// blocked) to the file that contributes/would contribute it ("loopy",
// ".mcp.json", or "codex") — codex wins over claude, loopy over both.
type Filtered struct {
	Merged  map[string]ServerConfig
	Blocked map[string]ServerConfig
	Sources map[string]string
	Errs    map[string]error
}

// LoadMergedFiltered discovers server configs like LoadMerged, then applies
// the import policy: filtered-out claude/codex entries land in Blocked as
// disabled+noted copies. loopyCfg entries always pass through.
func LoadMergedFiltered(cwd string, loopyCfg map[string]ServerConfig, policy ImportPolicy) Filtered {
	errs := map[string]error{}
	claude, err := LoadClaude(filepath.Join(cwd, ".mcp.json"))
	if err != nil && !os.IsNotExist(err) {
		errs[".mcp.json"] = err
	}
	codex, err := LoadCodex(CodexPath())
	if err != nil && !os.IsNotExist(err) {
		errs[CodexPath()] = err
	}
	blocked := map[string]ServerConfig{}
	split := func(src map[string]ServerConfig, p ImportSourcePolicy) map[string]ServerConfig {
		kept := make(map[string]ServerConfig, len(src))
		for name, c := range src {
			if !p.Admits(name) {
				if _, owned := loopyCfg[name]; !owned { // loopy always wins; no ghost row
					off := false
					c.Enabled = &off
					if c.Note != "" { // keep an existing import note (e.g. legacy sse)
						c.Note = "blocked by mcpImport config — " + c.Note
					} else {
						c.Note = "blocked by mcpImport config"
					}
					blocked[name] = c
				}
				continue
			}
			kept[name] = c
		}
		return kept
	}
	claudeKept := split(claude, policy.Claude)
	codexKept := split(codex, policy.Codex)
	sources := make(map[string]string, len(loopyCfg)+len(codex)+len(claude))
	for name := range loopyCfg {
		sources[name] = "loopy"
	}
	for name := range claude {
		sources[name] = ".mcp.json"
	}
	for name := range codex { // codex wins over claude in Merge
		sources[name] = "codex"
	}
	return Filtered{
		Merged:  Merge(loopyCfg, codexKept, claudeKept),
		Blocked: blocked,
		Sources: sources,
		Errs:    errs,
	}
}

// LoadMerged discovers MCP server configs from all supported sources and
// merges them: the project .mcp.json in cwd (claude-style), the codex config,
// then loopy's own config on top. cwd is the project directory; loopyCfg may
// be nil. Discovery failures (unreadable/unparseable files) are reported in
// errs, keyed by source path, and never abort the merge. No import policy is
// applied — both sources are imported wholesale.
func LoadMerged(cwd string, loopyCfg map[string]ServerConfig) (map[string]ServerConfig, map[string]error) {
	f := LoadMergedFiltered(cwd, loopyCfg, ImportPolicyFrom(nil))
	return f.Merged, f.Errs
}

// CodexPath is the codex CLI's config file location (~/.codex/config.toml).
// A variable so tests can point it at fixtures.
var CodexPath = defaultCodexPath

// FromConfigMap converts loopy's config-file MCP block (identical field
// shape, defined in internal/config to keep that package a leaf) into
// normalized server configs.
func FromConfigMap(in map[string]config.MCPServer) map[string]ServerConfig {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ServerConfig, len(in))
	for name, c := range in {
		out[name] = ServerConfig{
			Command:        c.Command,
			Env:            expandEnvMap(c.Env),
			Cwd:            c.Cwd,
			URL:            c.URL,
			Headers:        expandEnvMap(c.Headers),
			Enabled:        c.Enabled,
			Note:           c.Note,
			StartupTimeout: c.StartupTimeout,
			ToolTimeout:    c.ToolTimeout,
		}
	}
	return out
}

func defaultCodexPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}

// expandEnv resolves "$VAR" and "${VAR}" references in config values (claude
// does this in .mcp.json env blocks; codex expands env vars in its TOML too).
// Missing variables expand to "".
func expandEnv(v string) string {
	if !strings.Contains(v, "$") {
		return v
	}
	return os.Expand(v, os.Getenv)
}

func expandEnvMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = expandEnv(v)
	}
	return out
}
