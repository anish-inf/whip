// Package skills discovers SKILL.md files and renders them into the system prompt.
package skills

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill is one discovered skill.
type Skill struct {
	Name        string
	Description string
	Path        string // path to the SKILL.md
	// Warning is non-empty when the skill loaded but is degraded — e.g. the
	// description exceeds maxDesc and is truncated in the system prompt.
	// Surfaced in the startup report so a broken skill is never silent.
	Warning string
}

// ScanProblem is a SKILL.md that failed to load (bad frontmatter, unreadable).
// Scan used to skip these silently; pi's startup [Skill conflicts] block
// showed how valuable naming them is.
type ScanProblem struct {
	Path string
	Err  string
}

// DefaultDirs returns loopy's skill locations: project .agents/skills, then
// user ~/.loopy/skills.
func DefaultDirs() []string {
	var dirs []string
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, ".agents", "skills"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".loopy", "skills"))
	}
	return dirs
}

// Scan reads <dir>/<skill>/SKILL.md for each dir, skipping anything
// unreadable. Loaded-but-degraded skills carry a Warning (e.g. description
// truncated); anything that fails to parse is silently skipped (a SKILL.md
// with no frontmatter is usually just a stray doc) but counted — callers
// that want the conflicts view use ScanDetailed.
func Scan(dirs ...string) []Skill {
	sk, _ := ScanDetailed(dirs...)
	return sk
}

// ScanDetailed is Scan plus the problems found: directories whose SKILL.md
// exists but failed to parse, and parse-level warnings.
func ScanDetailed(dirs ...string) ([]Skill, []ScanProblem) {
	var out []Skill
	var problems []ScanProblem
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(d, e.Name(), "SKILL.md")
			if _, err := os.Stat(p); err != nil {
				continue // no SKILL.md: not a skill, not a problem
			}
			s, err := parse(p)
			if err != nil {
				problems = append(problems, ScanProblem{Path: p, Err: err.Error()})
				continue
			}
			if s.Name == "" {
				s.Name = e.Name()
			}
			if len(s.Description) > maxDesc {
				s.Warning = fmt.Sprintf("description exceeds %d characters (%d) — truncated in the system prompt", maxDesc, len(s.Description))
			}
			out = append(out, s)
		}
	}
	return out, problems
}

// parse reads name/description from the YAML frontmatter.
// ponytail: single-line values only; a real YAML parser when a skill needs one
func parse(path string) (Skill, error) {
	f, err := os.Open(path)
	if err != nil {
		return Skill{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return Skill{}, fmt.Errorf("%s: no frontmatter", path)
	}
	s := Skill{Path: path}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			s.Name = unquote(v)
		} else if v, ok := strings.CutPrefix(line, "description:"); ok {
			s.Description = unquote(v)
		}
	}
	return s, sc.Err()
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	for _, q := range []string{`"`, `'`} {
		if strings.HasPrefix(v, q) && strings.HasSuffix(v, q) && len(v) >= 2 {
			return v[1 : len(v)-1]
		}
	}
	return v
}

const maxDesc = 300 // keep the system prompt sane with wordy skills

// PromptBlock renders skills for the system prompt ("" when none).
func PromptBlock(sk []Skill) string {
	if len(sk) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<available_skills>\nThese skills hold task-specific instructions. When one is relevant, read its SKILL.md with the read tool and follow it.\n")
	for _, s := range sk {
		d := s.Description
		if len(d) > maxDesc {
			d = d[:maxDesc] + "…"
		}
		fmt.Fprintf(&b, "- %s: %s (%s)\n", s.Name, d, s.Path)
	}
	b.WriteString("</available_skills>")
	return b.String()
}
