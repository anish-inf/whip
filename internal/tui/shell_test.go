package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/llm"
)

func shellModel() *model {
	m := &model{
		input: newInput(),
		agent: &agent.Agent{},
	}
	m.width = 80
	m.input.SetWidth(m.width - 2)
	return m
}

func TestRunShellAppendsToolBlockAndMessage(t *testing.T) {
	m := shellModel()
	m.runShell("!echo hello")

	b := m.blocks[len(m.blocks)-1]
	if b.kind != blockTool {
		t.Fatalf("output should be a tool block, got kind %d", b.kind)
	}
	if !strings.Contains(b.text, "hello") {
		t.Fatalf("output should contain the command's stdout: %q", b.text)
	}

	if len(m.agent.Messages) != 1 {
		t.Fatalf("expected one conversation message, got %d", len(m.agent.Messages))
	}
	msg := m.agent.Messages[0]
	if msg.Role != "user" || msg.Authored {
		t.Fatalf("message should be a non-authored user message: %+v", msg)
	}
	if !strings.HasPrefix(msg.Content, "$ echo hello") {
		t.Fatalf("message should lead with the command: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "hello") {
		t.Fatalf("message should carry the output: %q", msg.Content)
	}
}

func TestRunShellEmptyIsANote(t *testing.T) {
	m := shellModel()
	m.runShell("!")
	if len(m.agent.Messages) != 0 {
		t.Fatalf("bare ! should not touch the conversation: %v", m.agent.Messages)
	}
	if b := lastBlock(m); !strings.Contains(b, "! <command>") {
		t.Fatalf("bare ! should print a usage note: %q", b)
	}
}

func TestRunShellFailingCommand(t *testing.T) {
	m := shellModel()
	m.runShell("!echo oops >&2; exit 3")
	b := lastBlock(m)
	if !strings.Contains(b, "oops") || !strings.Contains(b, "exit") {
		t.Fatalf("stderr and exit status should be captured: %q", b)
	}
}

func TestRunShellTruncatesHugeOutput(t *testing.T) {
	m := shellModel()
	m.runShell("!seq 1 200000")
	if b := lastBlock(m); !strings.Contains(b, "truncated") {
		t.Fatalf("huge output should carry a truncation marker (len %d)", len(b))
	}
}

func TestRunShellWhileBusySteers(t *testing.T) {
	// mid-turn the turn goroutine owns Messages, so the output is steered
	// (mutex-guarded) for injection at the next loop boundary instead of a
	// racy direct append
	m := shellModel()
	m.busy = true
	m.runShell("!echo mid-turn")
	if !m.busy {
		t.Fatal("runShell must not clear busy")
	}
	if len(m.agent.Messages) != 0 {
		t.Fatalf("mid-turn output must steer, not append: %v", m.agent.Messages)
	}
	// drainPending is unexported, but Turn would see it — pin via a turn on a
	// nil client being overkill; instead confirm the transcript got the block
	if b := lastBlock(m); !strings.Contains(b, "mid-turn") {
		t.Fatalf("the output block should still land in the transcript: %q", b)
	}
}

func TestEnterWhileBusyRunsShellEscape(t *testing.T) {
	m := busyQueueModel()
	m.input.SetValue("!echo hi")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.queue) != 0 {
		t.Fatalf("! should run now, not queue: %v", m.queue)
	}
	if len(m.agent.Messages) != 0 {
		t.Fatalf("busy output steers into the running turn, not append: %v", m.agent.Messages)
	}
	if b := lastBlock(m); !strings.Contains(b, "hi") {
		t.Fatalf("the output block should land: %q", b)
	}
	if m.hist[len(m.hist)-1] != "!echo hi" {
		t.Fatalf("the escape should be in history: %v", m.hist)
	}
}

func TestEnterIdleRunsShellEscape(t *testing.T) {
	m := shellModel()
	m.input.SetValue("!echo hi")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.busy {
		t.Fatal("! must not start a turn")
	}
	if len(m.agent.Messages) != 1 {
		t.Fatalf("the shell message should land: %d", len(m.agent.Messages))
	}
}

func TestQueueDrainExecutesShellEscape(t *testing.T) {
	m := busyQueueModel("!echo drained")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyUp}) // exercise queueSel reset
	tm, _ := m.Update(turnDoneMsg{})
	m = tm.(*model)
	if len(m.queue) != 0 {
		t.Fatalf("the drained ! line should execute, not resubmit: %v", m.queue)
	}
	if m.queueSel != -1 {
		t.Fatalf("queueSel should reset, got %d", m.queueSel)
	}
	// busy just cleared, so the drained escape appends idle-style
	if len(m.agent.Messages) != 1 || !strings.Contains(m.agent.Messages[0].Content, "drained") {
		t.Fatalf("the shell message should land: %+v", m.agent.Messages)
	}
	// the queued line was already rendered in the queue view; draining must
	// not re-echo it as ❯ !echo drained
	for _, b := range m.blocks {
		if strings.Contains(b.text, "❯ !echo drained") {
			t.Fatal("drained escapes should not re-echo the command line")
		}
	}
}

func TestCdAndPwd(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	m := shellModel()
	m.command("/pwd")
	if b := lastBlock(m); !strings.Contains(b, orig) {
		t.Fatalf("/pwd should print the cwd: %q", b)
	}

	// macOS: t.TempDir lives under /var, a symlink to /private/var — Getwd
	// resolves it, the literal dir doesn't.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.command("/cd " + dir)
	if wd, _ := os.Getwd(); wd != dir {
		t.Fatalf("/cd should chdir: got %q", wd)
	}
	if b := lastBlock(m); !strings.Contains(b, dir) {
		t.Fatalf("/cd should confirm the new cwd: %q", b)
	}

	// bare /cd prints
	m.command("/cd")
	if b := lastBlock(m); !strings.Contains(b, dir) {
		t.Fatalf("bare /cd should print the cwd: %q", b)
	}

	// bad dir errors without moving
	m.command("/cd /definitely/not/a/dir")
	if b := lastBlock(m); !strings.Contains(b, "/cd:") {
		t.Fatalf("/cd should report the error: %q", b)
	}
	if wd, _ := os.Getwd(); wd != dir {
		t.Fatalf("failed /cd should not move: got %q", wd)
	}
}

func TestCdTilde(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	m := shellModel()
	m.command("/cd ~")
	if wd, _ := os.Getwd(); wd != home {
		t.Fatalf("/cd ~ should land in $HOME: got %q", wd)
	}
}

func TestSeedTranscriptRendersShellMessage(t *testing.T) {
	m := shellModel()
	msgs := []llm.Message{{Role: "user", Content: "$ ls\nfoo.go bar.go"}}
	m.seedTranscript(msgs, 1)
	if b := lastBlock(m); !strings.Contains(b, "$ ls") {
		t.Fatalf("a resumed shell message should render: %q", b)
	}
}

// "! " with only spaces after the bang → usage note, no message
func TestBangWhitespaceOnlyIsANote(t *testing.T) {
	m := shellModel()
	m.input.SetValue("!   ")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.agent.Messages) != 0 {
		t.Fatalf("whitespace-only ! should be the usage note: %v", m.agent.Messages)
	}
}

// "!" not at offset 0 (e.g. pasted mid-line) — idle path trims and checks prefix
func TestBangNotAtStartQueuesAsMessage(t *testing.T) {
	m := busyQueueModel() // busy: plain text queues instead of submitting (no provider needed)
	m.input.SetValue("say ! loudly")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.queue) != 1 || m.queue[0] != "say ! loudly" {
		t.Fatalf("mid-string ! must queue as a plain message: %v", m.queue)
	}
	if len(m.agent.Messages) != 0 {
		t.Fatal("mid-string ! must not trigger the shell escape")
	}
}

// multiline command via ctrl+j
func TestRunShellMultilineCommand(t *testing.T) {
	m := shellModel()
	m.runShell("!echo a\necho b")
	if len(m.agent.Messages) != 1 || !strings.Contains(m.agent.Messages[0].Content, "b") {
		t.Fatalf("multiline command should run through bash -c: %+v", m.agent.Messages)
	}
}
