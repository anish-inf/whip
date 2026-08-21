package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/context-labs/loopy/internal/config"
)

const goalMetToken = "GOAL_MET"

// goalMaxRounds resolves the goal-loop round cap: per-project override
// (~/.loopy/projects.json, keyed by cwd) beats the global default
// (goalMaxRounds in ~/.loopy/config.json), which falls back to
// config.DefaultGoalMaxRounds. Set either with /goal rounds.
func (m *model) goalMaxRounds() int {
	if wd, err := os.Getwd(); err == nil {
		if n := config.ProjectGoalMaxRounds(wd); n > 0 {
			return n
		}
	}
	if m.cfg != nil && m.cfg.GoalMaxRounds > 0 {
		return m.cfg.GoalMaxRounds
	}
	return config.DefaultGoalMaxRounds
}

// goalContinuePrompt is sent after each completed turn while a goal is set.
// Continuing is the default — stopping requires the explicit token — which is
// what prevents the early-termination failure mode.
func goalContinuePrompt(goal string) string {
	return fmt.Sprintf(`[goal check] The session goal is:
%s

If the goal is FULLY accomplished and you have VERIFIED it with your tools (builds pass, tests pass, behavior confirmed), reply starting with exactly %s followed by a one-line summary.

Otherwise do not mention %s — keep working toward the goal right now with your tools. If any part is incomplete, unverified, or you are unsure, that means keep working. Do not stop to ask questions; make reasonable assumptions and proceed.`, goal, goalMetToken, goalMetToken)
}

// goalMet reports whether the model explicitly declared the goal done.
func goalMet(final string) bool {
	return strings.HasPrefix(strings.TrimSpace(final), goalMetToken)
}

// goalRoundsCommand implements /goal rounds: bare reports the effective cap
// and where it comes from, a number sets the per-project override (--global
// sets the config default instead), and "default" clears the override.
func (m *model) goalRoundsCommand(args []string) {
	global := false
	var num string
	for _, a := range args {
		if a == "--global" || a == "-g" {
			global = true
		} else if num == "" {
			num = a
		} else {
			m.append(errStyle.Render("usage: /goal rounds [n|default] [--global]"))
			return
		}
	}
	wd, _ := os.Getwd()
	proj := config.ProjectGoalMaxRounds(wd)
	cfgN := 0
	if m.cfg != nil {
		cfgN = m.cfg.GoalMaxRounds
	}

	switch num {
	case "":
		src := fmt.Sprintf("built-in default (%d)", config.DefaultGoalMaxRounds)
		if proj > 0 {
			src = "project override"
		} else if cfgN > 0 {
			src = "global config"
		}
		m.append(dimStyle.Render(fmt.Sprintf("◎ goal rounds: %d (%s) — /goal rounds <n>|default [--global]", m.goalMaxRounds(), src)))
		return
	case "default":
		// clear
	default:
		n := 0
		if _, err := fmt.Sscan(num, &n); err != nil || n <= 0 {
			m.append(errStyle.Render("rounds must be a positive number (or \"default\")"))
			return
		}
		if global {
			m.cfg.GoalMaxRounds = n
			if err := m.cfg.Save(); err != nil {
				m.append(errStyle.Render("couldn't save config: " + err.Error()))
				return
			}
			m.append(dimStyle.Render(fmt.Sprintf("◎ global goal rounds: %d%s", n, overriddenNote(proj))))
			return
		}
		if err := config.SetProjectGoalMaxRounds(wd, n); err != nil {
			m.append(errStyle.Render("couldn't save project override: " + err.Error()))
			return
		}
		m.append(dimStyle.Render(fmt.Sprintf("◎ goal rounds for this project: %d", n)))
		return
	}

	// "default": clear the override at the chosen scope
	if global {
		m.cfg.GoalMaxRounds = 0
		if err := m.cfg.Save(); err != nil {
			m.append(errStyle.Render("couldn't save config: " + err.Error()))
			return
		}
		m.append(dimStyle.Render(fmt.Sprintf("◎ global goal rounds reset to %d%s", config.DefaultGoalMaxRounds, overriddenNote(proj))))
		return
	}
	if err := config.SetProjectGoalMaxRounds(wd, 0); err != nil {
		m.append(errStyle.Render("couldn't save project override: " + err.Error()))
		return
	}
	m.append(dimStyle.Render(fmt.Sprintf("◎ project goal rounds cleared — using %d", m.goalMaxRounds())))
}

// overriddenNote flags when a project override still wins over a global change.
func overriddenNote(proj int) string {
	if proj > 0 {
		return fmt.Sprintf(" (this project overrides it with %d)", proj)
	}
	return ""
}
