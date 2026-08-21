package tui

// reasoning effort levels; "" means off (parameter omitted from requests)
var efforts = []string{"", "low", "medium", "high"}

var effortCands = []cand{
	{"off", "No reasoning effort parameter sent"},
	{"low", "Fast, shallow reasoning"},
	{"medium", "Balanced reasoning"},
	{"high", "Deep reasoning, slower"},
}

// nextEffort cycles off → low → medium → high → off.
func nextEffort(cur string) string {
	for i, e := range efforts {
		if e == cur {
			return efforts[(i+1)%len(efforts)]
		}
	}
	return efforts[0]
}

// effortLabel renders a level for display ("" shows as off).
func effortLabel(e string) string {
	if e == "" {
		return "off"
	}
	return e
}

// parseEffort validates user input ("off" maps to "").
func parseEffort(s string) (string, bool) {
	if s == "off" {
		return "", true
	}
	for _, e := range efforts[1:] {
		if s == e {
			return e, true
		}
	}
	return "", false
}
