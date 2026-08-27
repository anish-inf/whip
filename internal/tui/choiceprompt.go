package tui

import (
	"strconv"
	"strings"
)

// openChoicePrompt repurposes the input box as a one-shot numbered menu: the
// options are appended to the transcript and the user types a number (Enter to
// confirm). Used for the inference-net workspace/project pickers, where the
// set is small and known. It reuses the namePrompt slot so Enter/Esc handling
// and the input-box chrome come for free.
func (m *model) openChoicePrompt(title string, options []string, onOK func(string)) {
	var sb strings.Builder
	sb.WriteString(title + "\n")
	for i, o := range options {
		sb.WriteString(dimStyle.Render("  "+strconv.Itoa(i+1)+") "+o) + "\n")
	}
	sb.WriteString(dimStyle.Render("  type a number (enter for 1)"))
	m.append(sb.String())
	m.openNamePrompt(title+" [1]:", "", func(value string) {
		choice := resolveChoice(strings.TrimSpace(value), options)
		if choice == "" {
			return // invalid number: stay silent, prompt already closed
		}
		onOK(choice)
	})
	m.namePrompt.mask = false
}

// resolveChoice maps a typed value to an option: empty = first, a number =
// that entry, an exact name match = that option.
func resolveChoice(value string, options []string) string {
	if len(options) == 0 {
		return ""
	}
	if value == "" {
		return options[0]
	}
	if n, err := strconv.Atoi(value); err == nil && n >= 1 && n <= len(options) {
		return options[n-1]
	}
	for _, o := range options {
		if o == value {
			return o
		}
	}
	return ""
}
