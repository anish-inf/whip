package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/context-labs/loopy/internal/config"
	"github.com/context-labs/loopy/internal/mcp"
)

// mcpCLI implements `loopy mcp <list|add|remove|serve>`.
//
//	list                        merged view of every configured server and where it came from
//	add <name> -- <cmd...>      register a stdio server
//	add <name> --url <url>      register a remote (streamable HTTP) server
//	remove <name>               drop a server from loopy's own config
//	serve                       run loopy's tools as an MCP server over stdio
//
// add/remove write through config.Save (atomic, clobber-guarded). Servers
// imported from .mcp.json or codex can't be removed here (edit the source
// file); remove on an imported name explains that.
func mcpCLI(args []string, version string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: loopy mcp <list|add|remove|serve>")
	}
	if args[0] == "serve" {
		return mcp.Serve(context.Background(), version)
	}
	if args[0] == "test" {
		if len(args) < 2 {
			return fmt.Errorf("usage: loopy mcp test <name>")
		}
		return mcpTestCLI(args[1])
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		wd, _ := os.Getwd()
		merged, errs := mcp.LoadMerged(wd, mcp.FromConfigMap(cfg.MCPServers))
		loopyNames := map[string]bool{}
		for name := range cfg.MCPServers {
			loopyNames[name] = true
		}
		names := make([]string, 0, len(merged))
		for name := range merged {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Println("no MCP servers configured")
		}
		for _, name := range names {
			c := merged[name]
			src := "imported (.mcp.json/codex)"
			if loopyNames[name] {
				src = "loopy config"
			}
			target := strings.Join(c.Command, " ")
			if c.Remote() {
				target = c.URL
			}
			status := "enabled"
			if c.Disabled() {
				status = "disabled"
			}
			fmt.Printf("%-20s %-9s %-30s %s\n", name, status, target, src)
		}
		for src, e := range errs {
			fmt.Fprintf(os.Stderr, "mcp: %s: %s\n", src, e)
		}
		return nil

	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: loopy mcp add <name> -- <cmd...> | loopy mcp add <name> --url <url>")
		}
		name := args[1]
		entry := config.MCPServer{}
		rest := args[2:]
		switch {
		case len(rest) >= 2 && rest[0] == "--url":
			entry.URL = rest[1]
		case len(rest) >= 2 && rest[0] == "--":
			entry.Command = rest[1:]
		default:
			return fmt.Errorf("usage: loopy mcp add <name> -- <cmd...> | loopy mcp add <name> --url <url>")
		}
		sc := mcp.FromConfigMap(map[string]config.MCPServer{name: entry})[name]
		if msg := sc.Valid(); msg != "" {
			return fmt.Errorf("invalid server: %s", msg)
		}
		if cfg.MCPServers == nil {
			cfg.MCPServers = map[string]config.MCPServer{}
		}
		cfg.MCPServers[name] = entry
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("added mcp server %q — starts on next loopy launch\n", name)
		return nil

	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: loopy mcp remove <name>")
		}
		name := args[1]
		if _, ok := cfg.MCPServers[name]; !ok {
			// Maybe it's imported.
			wd, _ := os.Getwd()
			merged, _ := mcp.LoadMerged(wd, mcp.FromConfigMap(cfg.MCPServers))
			if _, imported := merged[name]; imported {
				return fmt.Errorf("%q comes from .mcp.json or ~/.codex/config.toml — edit that file to remove it", name)
			}
			return fmt.Errorf("no mcp server named %q", name)
		}
		delete(cfg.MCPServers, name)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("removed mcp server %q\n", name)
		return nil
	}
	return fmt.Errorf("unknown mcp subcommand %q (list|add|remove|serve|test)", args[0])
}

// mcpTestCLI is the doctor: connect to one configured server, report status,
// timing, tool names, and the stderr tail on failure. Exits non-zero when the
// server isn't usable, so CI can validate a .mcp.json before it ships.
func mcpTestCLI(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	wd, _ := os.Getwd()
	merged, _ := mcp.LoadMerged(wd, mcp.FromConfigMap(cfg.MCPServers))
	sc, ok := merged[name]
	if !ok {
		return fmt.Errorf("no mcp server named %q (try: loopy mcp list)", name)
	}
	fmt.Printf("testing mcp server %q (%s)…\n", name, mcpTarget(sc))
	res := mcp.Probe(context.Background(), name, sc)
	switch res.Status {
	case mcp.StatusReady:
		fmt.Printf("✓ connected in %s — %d tools\n", res.Elapsed.Round(time.Millisecond), res.Tools)
		if len(res.ToolNames) > 0 {
			fmt.Println("  tools:", strings.Join(res.ToolNames, ", "))
		}
		return nil
	case mcp.StatusDisabled:
		fmt.Println("○ disabled — enable it in ~/.loopy/config.json")
		return fmt.Errorf("server %q is disabled", name)
	default:
		fmt.Printf("✗ failed after %s: %s\n", res.Elapsed.Round(time.Millisecond), res.Err)
		if res.Note != "" {
			fmt.Println("  note:", res.Note)
		}
		return fmt.Errorf("server %q failed", name)
	}
}

func mcpTarget(c mcp.ServerConfig) string {
	if c.Remote() {
		return c.URL
	}
	return strings.Join(c.Command, " ")
}
