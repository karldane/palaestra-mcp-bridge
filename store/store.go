package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// Store wraps the SQLite database and provides typed CRUD operations for
// backends, users, user tokens, OAuth sessions, OAuth codes, and OAuth clients.
type Store struct {
	db *sql.DB
}

// New opens (or creates) a SQLite database at path and runs migrations.
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the raw *sql.DB for advanced use (e.g. transactions).
func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Incremental migrations for existing databases.
	// Each ALTER TABLE may fail with "duplicate column" — that is expected.
	s.db.Exec(`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user'`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN env_mappings TEXT NOT NULL DEFAULT '{}'`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN is_system INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN min_pool_size INTEGER NOT NULL DEFAULT 1`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN max_pool_size INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN tool_hints TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN backend_instructions TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)

	// Migration: rename token to session_id in web_sessions
	// SQLite doesn't support ALTER TABLE rename column, so we handle this in queries
	// The code below checks and migrates if needed
	var hasOldSchema int
	s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('web_sessions') WHERE name='token'`).Scan(&hasOldSchema)
	if hasOldSchema > 0 {
		s.db.Exec(`ALTER TABLE web_sessions RENAME TO web_sessions_old`)
		s.db.Exec(`
			CREATE TABLE web_sessions (
				session_id TEXT PRIMARY KEY,
				user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				expires_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			INSERT INTO web_sessions (session_id, user_id, expires_at, created_at)
			SELECT token, user_id, expires_at, created_at FROM web_sessions_old;
			DROP TABLE web_sessions_old;
		`)
	}

	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS backends (
	id          TEXT PRIMARY KEY,
	command     TEXT NOT NULL,
	pool_size   INTEGER NOT NULL DEFAULT 1,
	tool_prefix TEXT NOT NULL DEFAULT '',
	env         TEXT NOT NULL DEFAULT '{}',
	env_mappings TEXT NOT NULL DEFAULT '{}',
	tool_hints  TEXT NOT NULL DEFAULT '',
	backend_instructions TEXT NOT NULL DEFAULT '',
	enabled     INTEGER NOT NULL DEFAULT 1,
	is_system   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS users (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	email      TEXT NOT NULL DEFAULT '',
	password   TEXT NOT NULL DEFAULT '',
	role       TEXT NOT NULL DEFAULT 'user',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(email)
);

CREATE TABLE IF NOT EXISTS user_tokens (
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	backend_id TEXT NOT NULL REFERENCES backends(id) ON DELETE CASCADE,
	env_key    TEXT NOT NULL,
	value      TEXT NOT NULL,
	PRIMARY KEY (user_id, backend_id, env_key)
);

CREATE TABLE IF NOT EXISTS oauth_clients (
	client_id     TEXT PRIMARY KEY,
	client_secret TEXT NOT NULL DEFAULT '',
	redirect_uris TEXT NOT NULL DEFAULT '[]',
	client_name   TEXT NOT NULL DEFAULT '',
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS oauth_codes (
	code           TEXT PRIMARY KEY,
	user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client_id      TEXT NOT NULL,
	redirect_uri   TEXT NOT NULL,
	code_challenge TEXT NOT NULL DEFAULT '',
	scope          TEXT NOT NULL DEFAULT '',
	expires_at     DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_sessions (
	access_token  TEXT PRIMARY KEY,
	refresh_token TEXT NOT NULL DEFAULT '',
	user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client_id     TEXT NOT NULL,
	scope         TEXT NOT NULL DEFAULT '',
	expires_at    DATETIME NOT NULL,
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS web_sessions (
	session_id TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at DATETIME NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name        TEXT NOT NULL DEFAULT '',
	key_hash    TEXT NOT NULL,
	expires_at  DATETIME,
	last_used_at DATETIME,
	created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Cached capabilities for each backend (global - same tools for all users)
CREATE TABLE IF NOT EXISTS backend_capabilities (
	backend_id  TEXT PRIMARY KEY REFERENCES backends(id) ON DELETE CASCADE,
	tools       TEXT NOT NULL,
	tool_count  INTEGER NOT NULL DEFAULT 0,
	updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Global settings for MCP Bridge
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// ---------- Helpers ----------

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// GenerateID returns a random hex string (exported for external use).
func GenerateID() string {
	return generateID()
}
