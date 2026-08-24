package skills

import (
	"fmt"
	"testing"
)

// Every skill that ships with the repo must be Agent Skills spec-clean:
// valid name, description ≤1024, parseable frontmatter. This test is the
// ratchet — the startup report warns on violations, and this fails CI.
func TestRepoSkillsSpecClean(t *testing.T) {
	sk, problems := ScanDetailed("../../.agents/skills")
	for _, p := range problems {
		t.Errorf("unparseable skill: %s: %s", p.Path, p.Err)
	}
	for _, s := range sk {
		if s.Warning != "" {
			t.Errorf("%s: %s", s.Name, s.Warning)
		}
	}
}

// The block total should stay sane: with ~50 skills at ≤300 chars each the
// prompt block is ≲4k tokens. If someone adds 50 more skills this fails and
// forces a conversation about the budget.
func TestSkillBlockBudget(t *testing.T) {
	sk := Scan("../../.agents/skills")
	block := PromptBlock(sk)
	const budget = 30_000 // ≈7.5k tokens
	if len(block) > budget {
		t.Errorf("skills block = %d chars (budget %d)", len(block), budget)
	}
	fmt.Printf("skills block: %d chars across %d skills\n", len(block), len(sk))
}
