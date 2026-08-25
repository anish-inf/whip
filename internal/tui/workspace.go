package tui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/context-labs/whip/internal/config"
)

// Workspace rewind: each turn snapshots the working tree BEFORE it runs —
// `git stash create` on a dirty tree, a bare `commit-tree` of HEAD on a clean
// one — and pins the commit under refs/whip/snapshots/ so it isn't GC'd.
// The ref is recorded in the session store keyed by the conversation index
// the turn started at. A conversation rewind to before that index then also
// restores the files: checkout the snapshot's tree + delete the pin ref.
// Untracked files are never in the snapshot, so the user's own new files
// survive a rewind untouched.
//
// Everything here is best-effort and quiet: outside a git repo, before the
// first commit, or on any git error, snapshotting just no-ops — conversation
// rewind works exactly as before.

// snapshotWorkspace captures the working tree as a pinned commit. "" means
// "no snapshot": not a git repo, or no commits yet.
func snapshotWorkspace() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	commit, err := gitOut(ctx, "stash", "create")
	if err != nil {
		return ""
	}
	if commit == "" { // clean tree: pin HEAD's tree directly
		commit, err = gitOut(ctx, "commit-tree", "HEAD^{tree}", "-m", "whip turn snapshot")
		if err != nil {
			return ""
		}
	}
	if _, err := gitOut(ctx, "update-ref", "refs/whip/snapshots/"+commit, commit); err != nil {
		return ""
	}
	return commit
}

// workspaceClean reports whether tracked files match HEAD (untracked files
// don't count — snapshots never touch them).
func workspaceClean() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := gitOut(ctx, "status", "--porcelain", "--untracked-files=no")
	return err == nil && out == ""
}

// dropSnapshot deletes a snapshot's pin ref (the commit is then GC-able).
func dropSnapshot(ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = gitOut(ctx, "update-ref", "-d", "refs/whip/snapshots/"+ref)
}

// restoreWorkspace puts the working tree back to the snapshot's tracked-file
// state and deletes the pin ref. Dirty hand-edits are blown away by the
// checkout — that is the restore; untracked files are left alone. The count
// of dirty tracked files before the restore feeds the transcript note (they
// are exactly the files the checkout rewrote).
func restoreWorkspace(ref string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dirty, err := gitOut(ctx, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return 0, err
	}
	if _, err := gitOut(ctx, "checkout", ref, "--", "."); err != nil {
		return 0, err
	}
	dropSnapshot(ref)
	if dirty == "" {
		return 0, nil
	}
	return len(strings.Split(dirty, "\n")), nil
}

// gitOut runs git in the session's working directory and returns trimmed
// stdout. Errors carry stderr's first line so the transcript note can say
// why a restore failed without a wall of text.
func gitOut(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = cwd()
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(errb.String()), "\n")
		if line == "" {
			line = err.Error()
		}
		config.LogEvent("workspace.git", strings.Join(args, " ")+": "+line)
		return "", fmt.Errorf("%s", line)
	}
	return strings.TrimSpace(out.String()), nil
}
