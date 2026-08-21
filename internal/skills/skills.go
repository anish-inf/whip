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

// Scan reads <dir>/<skill>/SKILL.md for each dir, skipping anything unreadable.
func Scan(dirs ...string) []Skill {
	var out []Skill
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name(), "SKILL.md")
			s, err := parse(p)
			if err != nil {
				continue
			}
			if s.Name == "" {
				s.Name = e.Name()
			}
			out = append(out, s)
		}
	}
	return out
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
