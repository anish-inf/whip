package tools

import "testing"

func TestCommandRule(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ls -la", "ls"},
		{"git checkout main", "git checkout"},
		{"git", "git"},
		{"npm run build --watch", "npm run build"},
		{"docker compose up -d", "docker compose up"},
		{"git submodule update --init", "git submodule update"},
		// only the first command of a chain/pipeline is the rule
		{"git checkout main && rm -rf /", "git checkout"},
		{"cat foo | grep bar", "cat"},
		{"echo hi > out.txt", "echo"},
		{"ls; rm -rf /", "ls"},
		// leading env assignments are stripped
		{"FOO=1 BAR=2 git status", "git status"},
		{"  ", ""},
		{"FOO=1", ""},
	}
	for _, c := range cases {
		if got := CommandRule(c.in); got != c.want {
			t.Errorf("CommandRule(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
