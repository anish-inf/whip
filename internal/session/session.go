// Package session persists chat histories in ~/.loopy/sessions.db (SQLite).
package session

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/abe/loopy/internal/llm"
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
);`

// Meta is a session's bookkeeping row.
type Meta struct {
	ID        string
	Title     string
	Model     string
	Provider  string
	CWD       string
	Goal      string
	UpdatedAt time.Time
}

type Store struct{ db *sql.DB }

// Open opens (creating if needed) the sessions database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	// migrate pre-goal databases; duplicate-column errors are expected
	db.Exec(`ALTER TABLE sessions ADD COLUMN goal TEXT NOT NULL DEFAULT ''`)
	return &Store{db: db}, nil
}

// SetGoal stores the session's active goal ("" clears it).
func (s *Store) SetGoal(id, goal string) error {
	_, err := s.db.Exec(`UPDATE sessions SET goal=? WHERE id=?`, goal, id)
	return err
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
			title = truncate(strings.Join(strings.Fields(m.Content), " "), 64)
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
	rows, err := s.db.Query(`SELECT id, title, model, provider, cwd, goal, updated_at FROM sessions WHERE id LIKE ?||'%' LIMIT 3`, idOrPrefix)
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

	mrows, err := s.db.Query(`SELECT content FROM messages WHERE session_id=? ORDER BY seq`, meta.ID)
	if err != nil {
		return Meta{}, nil, err
	}
	defer mrows.Close()
	var msgs []llm.Message
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
	rows, err := s.db.Query(`SELECT id, title, model, provider, cwd, goal, updated_at FROM sessions
		WHERE EXISTS (SELECT 1 FROM messages WHERE session_id = sessions.id)
		ORDER BY updated_at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	return scanMetas(rows)
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
				*q.dst = m.Content
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

func scanMetas(rows *sql.Rows) ([]Meta, error) {
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var m Meta
		var updated string
		if err := rows.Scan(&m.ID, &m.Title, &m.Model, &m.Provider, &m.CWD, &m.Goal, &updated); err != nil {
			return nil, err
		}
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
