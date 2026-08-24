package tui

import (
	"encoding/json"
	"strings"
)

// browserStepLabel extracts the step label from browser_exec args: the
// first line of `code` that starts with "# " (the convention the tool
// description teaches — the model writes a plain-language label for the
// user, max 60 chars). Returns "" when absent.
func browserStepLabel(argsJSON string) string {
	var a struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil || a.Code == "" {
		return ""
	}
	for _, line := range strings.Split(a.Code, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimPrefix(line, "#"))
		}
		if line != "" {
			break // first non-comment line: no label
		}
	}
	return ""
}
