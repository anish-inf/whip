// Package session persists chat histories in ~/.loopy/sessions.db (SQLite).
package session

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/context-labs/loopy/internal/llm"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	cwd        TEXT NOT NULL,
	model      TEXT NOT NULL,
	provider   TEXT NOT NULL,
	title      TEXT NOT NULL DEFAULT '',
	goal       TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS messages (
	session_id TEXT NOT NULL REFERENCES sessions(id),
	seq        INTEGER NOT NULL,
	role       TEXT NOT NULL,
	content    TEXT NOT NULL, -- llm.Message JSON
	PRIMARY KEY (session_id, seq)
);
CREATE TABLE IF NOT EXISTS tasks (
	session_id  TEXT NOT NULL REFERENCES sessions(id),
	task_id     TEXT NOT NULL,
	description TEXT NOT NULL,
	prompt      TEXT NOT NULL,
	status      TEXT NOT NULL,
	report      TEXT NOT NULL DEFAULT '',
	started_at  TEXT NOT NULL,
	ended_at    TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, task_id)
);`

// extraColumns are added idempotently after the base schema: SQLite's
// ADD COLUMN errors if the column already exists, so each is guarded by an
// information check in migrate(). New per-session bookkeeping lands here, not
// in the CREATE above (which only runs on a fresh DB).
var extraColumns = []struct{ name, def string }{
	{"forked_from", "forked_from TEXT NOT NULL DEFAULT ''"},     // source session id
	{"fork_seq", "fork_seq INTEGER NOT NULL DEFAULT 0"},         // branch point in the source
	{"tags", "tags TEXT NOT NULL DEFAULT ''"},                   // comma-separated labels
	{"pinned", "pinned INTEGER NOT NULL DEFAULT 0"},             // 1 = keep / sort first
	{"effort", "effort TEXT NOT NULL DEFAULT ''"},               // reasoning effort in effect ("" = global default)
	{"usage_in", "usage_in INTEGER NOT NULL DEFAULT 0"},         // cumulative input tokens (provider-reported)
	{"usage_cached", "usage_cached INTEGER NOT NULL DEFAULT 0"}, // of usage_in, tokens served from the prompt cache
	{"usage_out", "usage_out INTEGER NOT NULL DEFAULT 0"},       // cumulative output tokens
}

// Meta is a session's bookkeeping row.
type Meta struct {
	ID          string
	Title       string
	Model       string
	Provider    string
	CWD         string
	Goal        string
	ForkedFrom  string   // source session id when created by /fork ("" = root)
	ForkSeq     int      // conversation index the fork branched at
	Tags        []string // freeform labels, for filtering /resume
	Pinned      bool     // pinned sessions sort first and survive cleanup
	Effort      string   // reasoning effort for this session ("" = use the global default)
	UsageIn     int      // cumulative input tokens across the session's API calls
	UsageCached int      // of UsageIn, tokens served from the provider's prompt cache
	UsageOut    int      // cumulative output tokens
	UpdatedAt   time.Time
}

type Store struct{ db *sql.DB }

// Open opens (creating if needed) the sessions database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",   // faster commits, no read/write blocking
		"PRAGMA synchronous=NORMAL", // safe in WAL; skips per-commit fsync
		"PRAGMA temp_store=MEMORY",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, err
		}
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	// migrate pre-goal databases; duplicate-column errors are expected
	db.Exec(`ALTER TABLE sessions ADD COLUMN goal TEXT NOT NULL DEFAULT ''`)
	// later per-session bookkeeping (fork linkage, tags, pinned); the same
	// duplicate-column-tolerant migration as goal
	for _, c := range extraColumns {
		db.Exec(`ALTER TABLE sessions ADD COLUMN ` + c.def)
	}
	return &Store{db: db}, nil
}

// SetGoal stores the session's active goal ("" clears it).
func (s *Store) SetGoal(id, goal string) error {
	_, err := s.db.Exec(`UPDATE sessions SET goal=? WHERE id=?`, goal, id)
	return err
}

// SetEffort stores the session's reasoning effort. "" means the row pre-dates
// per-session effort or never set one: resume falls back to the current global
// default and stamps it on the next save.
func (s *Store) SetEffort(id, effort string) error {
	_, err := s.db.Exec(`UPDATE sessions SET effort=? WHERE id=?`, effort, id)
	return err
}

// SetUsage stores the session's cumulative token totals (absolute values, not
// deltas) so a resumed session keeps its spend across restarts and
// compactions. Rows from before this column existed read as zero and get
// stamped with real totals on the next save.
func (s *Store) SetUsage(id string, in, cached, out int) error {
	_, err := s.db.Exec(`UPDATE sessions SET usage_in=?, usage_cached=?, usage_out=? WHERE id=?`, in, cached, out, id)
	return err
}

// Task is one background subagent's persisted record. It deliberately
// mirrors agent.BackgroundTask's exported fields without importing agent
// (session is a leaf; the TUI converts between them).
type Task struct {
	ID          string
	Description string
	Prompt      string
	Status      string // "running", "done", "error", "cancelled"
	Report      string
	StartedAt   time.Time
	EndedAt     time.Time
}

// SaveTask upserts a background subagent's record for a session. Called on
// start and on settle, so the final row holds the settled status/report.
func (s *Store) SaveTask(sessionID string, t Task) error {
	ended := ""
	if !t.EndedAt.IsZero() {
		ended = t.EndedAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO tasks
		(session_id, task_id, description, prompt, status, report, started_at, ended_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		sessionID, t.ID, t.Description, t.Prompt, t.Status, t.Report,
		t.StartedAt.UTC().Format(time.RFC3339), ended)
	return err
}

// LoadTasks returns a session's persisted background subagents, oldest first.
func (s *Store) LoadTasks(sessionID string) ([]Task, error) {
	rows, err := s.db.Query(`SELECT task_id, description, prompt, status, report, started_at, ended_at
		FROM tasks WHERE session_id=? ORDER BY started_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var started, ended string
		if err := rows.Scan(&t.ID, &t.Description, &t.Prompt, &t.Status, &t.Report, &started, &ended); err != nil {
			return nil, err
		}
		t.StartedAt, _ = time.Parse(time.RFC3339, started)
		if ended != "" {
			t.EndedAt, _ = time.Parse(time.RFC3339, ended)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Create inserts a new session and returns its id.
func (s *Store) Create(cwd, model, provider string) (string, error) {
	b := make([]byte, 4)
	rand.Read(b)
	id := hex.EncodeToString(b)
	_, err := s.db.Exec(`INSERT INTO sessions (id, created_at, updated_at, cwd, model, provider) VALUES (?,?,?,?,?,?)`,
		id, now(), now(), cwd, model, provider)
	return id, err
}

// Save persists msgs[from:] (the conversation without the system prompt) and
// refreshes the session's metadata.
func (s *Store) Save(id string, from int, msgs []llm.Message, model, provider string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := from; i < len(msgs); i++ {
		data, err := json.Marshal(msgs[i])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO messages (session_id, seq, role, content) VALUES (?,?,?,?)`,
			id, i, msgs[i].Role, string(data)); err != nil {
			return err
		}
	}
	title := ""
	for _, m := range msgs {
		if m.Role == "user" {
			title = truncate(strings.Join(strings.Fields(m.TextContent()), " "), 64)
			break
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at=?, model=?, provider=?, title=CASE WHEN title='' THEN ? ELSE title END WHERE id=?`,
		now(), model, provider, title, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Load resolves idOrPrefix to a session and returns its metadata and messages.
func (s *Store) Load(idOrPrefix string) (Meta, []llm.Message, error) {
	rows, err := s.db.Query(`SELECT id, title, model, provider, cwd, goal, forked_from, fork_seq, tags, pinned, effort, usage_in, usage_cached, usage_out, updated_at FROM sessions WHERE id LIKE ?||'%' LIMIT 3`, idOrPrefix)
	if err != nil {
		return Meta{}, nil, err
	}
	metas, err := scanMetas(rows)
	if err != nil {
		return Meta{}, nil, err
	}
	switch len(metas) {
	case 0:
		return Meta{}, nil, fmt.Errorf("no session matching %q", idOrPrefix)
	case 1:
	default:
		return Meta{}, nil, fmt.Errorf("session id %q is ambiguous", idOrPrefix)
	}
	meta := metas[0]

	// pre-size the slice: a long session is hundreds of rows; the COUNT is
	// one index scan and avoids O(log n) reallocs while scanning
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=?`, meta.ID).Scan(&count)

	mrows, err := s.db.Query(`SELECT content FROM messages WHERE session_id=? ORDER BY seq`, meta.ID)
	if err != nil {
		return Meta{}, nil, err
	}
	defer mrows.Close()
	msgs := make([]llm.Message, 0, count)
	for mrows.Next() {
		var data string
		if err := mrows.Scan(&data); err != nil {
			return Meta{}, nil, err
		}
		var m llm.Message
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			return Meta{}, nil, err
		}
		msgs = append(msgs, m)
	}
	return meta, msgs, mrows.Err()
}

// Recent returns up to n sessions, newest first.
func (s *Store) Recent(n int) ([]Meta, error) {
	rows, err := s.db.Query(`SELECT id, title, model, provider, cwd, goal, forked_from, fork_seq, tags, pinned, effort, usage_in, usage_cached, usage_out, updated_at FROM sessions
		WHERE EXISTS (SELECT 1 FROM messages WHERE session_id = sessions.id)
		ORDER BY updated_at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	return scanMetas(rows)
}

// UserHistory returns user-message contents across ALL sessions (every folder),
// newest first and de-duplicated, for up-arrow input recall. Order is by the
// session's last activity then the message's position within it, so the most
// recently typed input comes first. Only messages the human actually typed are
// recalled: steered background-task
// results and goal-continuation prompts are stored as role "user" too, but
// they're injected by loopy, not written by the user. Those carry Authored=false
// and are skipped; only Authored=true messages come back.
func (s *Store) UserHistory(limit int) ([]string, error) {
	rows, err := s.db.Query(`SELECT m.content FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE m.role='user'
		ORDER BY s.updated_at DESC, m.seq DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			continue // skip malformed rows rather than fail the whole recall
		}
		if !msg.Authored {
			continue // injected by loopy (steered task result / goal prompt), not typed
		}
		content := strings.TrimSpace(msg.TextContent())
		if content == "" || seen[content] {
			continue
		}
		seen[content] = true
		out = append(out, content)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// LastExchange returns the text of the session's last user message and last
// assistant response, for previews.
func (s *Store) LastExchange(id string) (user, assistant string) {
	for _, q := range []struct {
		role string
		dst  *string
	}{{"user", &user}, {"assistant", &assistant}} {
		var data string
		if err := s.db.QueryRow(`SELECT content FROM messages WHERE session_id=? AND role=? ORDER BY seq DESC LIMIT 1`,
			id, q.role).Scan(&data); err == nil {
			var m llm.Message
			if json.Unmarshal([]byte(data), &m) == nil {
				*q.dst = m.TextContent()
			}
		}
	}
	return user, assistant
}

// ClearMessages deletes the stored message rows for a session (the session
// row is kept). Used after compaction rewrites history: the compacted
// messages are smaller and re-seqenced from 0, so the old rows must go first.
func (s *Store) ClearMessages(id string) error {
	_, err := s.db.Exec(`DELETE FROM messages WHERE session_id=?`, id)
	return err
}

// DeleteFrom drops every stored message with seq >= from. seq equals the
// conversation index (Save persists msgs[i] at seq i; the system prompt is
// never persisted). Used by rewind: the clipped tail is deleted from disk
// but kept in memory for forward travel.
func (s *Store) DeleteFrom(id string, from int) error {
	_, err := s.db.Exec(`DELETE FROM messages WHERE session_id=? AND seq>=?`, id, from)
	return err
}

// SetTitle retitles a session (/rename).
func (s *Store) SetTitle(id, title string) error {
	_, err := s.db.Exec(`UPDATE sessions SET title=? WHERE id=?`, title, id)
	return err
}

// Fork copies a session's stored rows with seq <= uptoSeq (pass len(msgs)
// for a full copy — one past the last row) into a new session titled title,
// carrying over cwd/model/provider/goal, and returns the new id. seq equals
// the conversation index (the system prompt is never persisted). The source
// session is untouched. The rows are cloned in one INSERT…SELECT, so the DB
// does the copy; nothing round-trips through Go.
func (s *Store) Fork(srcID string, uptoSeq int, title string) (string, error) {
	b := make([]byte, 4)
	rand.Read(b)
	newID := hex.EncodeToString(b)
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO sessions (id, created_at, updated_at, cwd, model, provider, title, goal, forked_from, fork_seq, effort)
		SELECT ?, ?, ?, cwd, model, provider, ?, goal, ?, ?, effort FROM sessions WHERE id=?`,
		newID, now(), now(), title, srcID, uptoSeq, srcID); err != nil {
		return "", err
	}
	if uptoSeq > 0 {
		if _, err := tx.Exec(`INSERT INTO messages (session_id, seq, role, content)
			SELECT ?, seq, role, content FROM messages WHERE session_id=? AND seq <= ?`,
			newID, srcID, uptoSeq); err != nil {
			return "", err
		}
	}
	return newID, tx.Commit()
}

// SetTags replaces a session's label set (comma-separated storage).
func (s *Store) SetTags(id string, tags []string) error {
	_, err := s.db.Exec(`UPDATE sessions SET tags=? WHERE id=?`, strings.Join(tags, ","), id)
	return err
}

// SetPinned marks a session pinned (sorts first in /resume, kept by cleanup).
func (s *Store) SetPinned(id string, pinned bool) error {
	v := 0
	if pinned {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE sessions SET pinned=? WHERE id=?`, v, id)
	return err
}

// ForksOf lists sessions forked from id, newest first — the session tree's
// children of one node.
func (s *Store) ForksOf(id string) ([]Meta, error) {
	rows, err := s.db.Query(`SELECT id, title, model, provider, cwd, goal, forked_from, fork_seq, tags, pinned, effort, usage_in, usage_cached, usage_out, updated_at
		FROM sessions WHERE forked_from=? ORDER BY updated_at DESC`, id)
	if err != nil {
		return nil, err
	}
	return scanMetas(rows)
}

// ForkTitle derives the default fork name: "<title> (fork #N)" with N
// incremented past any existing fork of the same base (opencode's
// getForkedTitle — packages/opencode/src/session/session.ts:162). Falls back
// to "session (fork #1)" for untitled sessions.
func (s *Store) ForkTitle(base string) (string, error) {
	if base == "" {
		base = "session"
	}
	// unwrap an existing "(fork #N)" suffix so forks of forks increment
	// instead of nesting: "x (fork #2)" → "x (fork #3)", not "x (fork #2) (fork #1)"
	base = strings.TrimSpace(base)
	if i := strings.LastIndex(base, " (fork #"); i > 0 {
		var n0 int
		var rest string
		n, err := fmt.Sscanf(base[i:], " (fork #%d)%s", &n0, &rest)
		if n0 > 0 && rest == "" && (err == nil || err == io.EOF) && n >= 1 {
			base = base[:i]
		}
	}
	rows, err := s.db.Query(`SELECT title FROM sessions WHERE title = ? OR title LIKE ? ESCAPE '\'`,
		base, likeEscape(base)+` (fork #%)`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return "", err
		}
		var num int
		var rest string
		// exact suffix match only: a manually renamed "x (fork #9) notes"
		// must not inflate the numbering
		if nf, err := fmt.Sscanf(t, base+" (fork #%d)%s", &num, &rest); num > n && rest == "" && nf >= 1 && (err == nil || err == io.EOF) {
			n = num
		}
	}
	return fmt.Sprintf("%s (fork #%d)", base, n+1), rows.Err()
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func scanMetas(rows *sql.Rows) ([]Meta, error) {
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var m Meta
		var updated, tags string
		var pinned int
		if err := rows.Scan(&m.ID, &m.Title, &m.Model, &m.Provider, &m.CWD, &m.Goal,
			&m.ForkedFrom, &m.ForkSeq, &tags, &pinned, &m.Effort,
			&m.UsageIn, &m.UsageCached, &m.UsageOut, &updated); err != nil {
			return nil, err
		}
		if tags != "" {
			m.Tags = strings.Split(tags, ",")
		}
		m.Pinned = pinned != 0
		m.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, m)
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
