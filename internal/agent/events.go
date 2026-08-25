package agent

import "github.com/context-labs/whip/internal/llm"

// FanIn multiplexes several Events values into one: every fired callback is
// invoked on each source that implements it. A background worker runs its
// Turn with FanIn(registry.emitter(id), Events{OnUsage: …}) so the TUI's
// per-task subscribers and the parent's usage accounting both see the live
// stream.
func FanIn(evs ...Events) Events {
	return Events{
		OnText: func(s string) {
			for _, e := range evs {
				if e.OnText != nil {
					e.OnText(s)
				}
			}
		},
		OnThink: func(s string) {
			for _, e := range evs {
				if e.OnThink != nil {
					e.OnThink(s)
				}
			}
		},
		OnToolStart: func(name, args string) {
			for _, e := range evs {
				if e.OnToolStart != nil {
					e.OnToolStart(name, args)
				}
			}
		},
		OnToolEnd: func(name, result string) {
			for _, e := range evs {
				if e.OnToolEnd != nil {
					e.OnToolEnd(name, result)
				}
			}
		},
		OnSteer: func(text string) {
			for _, e := range evs {
				if e.OnSteer != nil {
					e.OnSteer(text)
				}
			}
		},
		OnCompact: func(took, kept int) {
			for _, e := range evs {
				if e.OnCompact != nil {
					e.OnCompact(took, kept)
				}
			}
		},
		OnUsage: func(u llm.Usage) {
			for _, e := range evs {
				if e.OnUsage != nil {
					e.OnUsage(u)
				}
			}
		},
	}
}
